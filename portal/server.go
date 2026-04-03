package portal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/keyless_tls/relay/l4"
	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"

	"github.com/gosuda/portal/v2/portal/acme"
	"github.com/gosuda/portal/v2/portal/discovery"
	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/portal/ols"
	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/portal/transport"
	"github.com/gosuda/portal/v2/portal/wireguard"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

const (
	defaultLeaseTTL           = 30 * time.Second
	defaultClaimTimeout       = 10 * time.Second
	defaultIdleKeepalive      = 15 * time.Second
	defaultReadyQueueLimit    = 8
	defaultClientHelloWait    = 2 * time.Second
	defaultControlBodyLimit   = 4 << 20
	defaultUDPPortBase        = 50000
	defaultWGRecoveryFailures = 3
)

type ServerConfig struct {
	PortalURL           string
	IdentityPath        string
	Bootstraps          []string
	WireGuardPrivateKey string
	DiscoveryPort       int
	WireGuardPublicKey  string
	WireGuardEndpoint   string
	OverlayIPv4         string
	OverlayCIDRs        []string
	ACME                acme.Config
	APIPort             int
	SNIPort             int
	APIListenAddr       string
	SNIListenAddr       string
	QUICListenAddr      string
	TrustedProxyCIDRs   string
	TrustProxyHeaders   bool
	DiscoveryEnabled    bool
	UDPPortCount        int
	I2PProxyURL         string
	I2PDiscoveryOnly    bool
}

type Server struct {
	sniListener        net.Listener
	apiListener        net.Listener
	apiServer          *http.Server
	apiTLSClose        io.Closer
	acmeManager        *acme.Manager
	quicTunnel         *quic.Listener
	overlay            *wireguard.Overlay
	interRelayListener net.Listener
	cancel             context.CancelFunc
	group              *errgroup.Group
	registry           *leaseRegistry
	ports              *transport.PortAllocator
	loadMgr            *policy.LoadManager
	weightMgr          *policy.WeightManager
	identity           types.Identity
	wgConfig           wireguard.Config
	cfg                ServerConfig
	trustedProxyCIDRs  []*net.IPNet
	relaySet           *discovery.RelaySet
	shutdownOnce       sync.Once
	olsManager         *discovery.OLSManager
	ols                *ols.Engine
	discoveryClient    *http.Client
}

func NewServer(cfg ServerConfig) (*Server, error) {
	cfg.PortalURL = strings.TrimSuffix(strings.TrimSpace(cfg.PortalURL), "/")
	cfg.APIPort = utils.IntOrDefault(cfg.APIPort, 4017)
	cfg.SNIPort = utils.IntOrDefault(cfg.SNIPort, 443)
	cfg.APIListenAddr = utils.StringOrDefault(cfg.APIListenAddr, fmt.Sprintf(":%d", cfg.APIPort))
	cfg.SNIListenAddr = utils.StringOrDefault(cfg.SNIListenAddr, fmt.Sprintf(":%d", cfg.SNIPort))
	rootHost := utils.PortalRootHost(cfg.PortalURL)
	if rootHost == "" {
		return nil, errors.New("root host is required")
	}
	cfg.QUICListenAddr = utils.StringOrDefault(cfg.QUICListenAddr, cfg.SNIListenAddr)
	trustedProxyCIDRs, err := utils.ParseCIDRs(cfg.TrustedProxyCIDRs)
	if err != nil {
		return nil, fmt.Errorf("parse trusted proxy cidrs: %w", err)
	}
	bootstraps, err := utils.NormalizeRelayURLs(cfg.Bootstraps...)
	if err != nil {
		return nil, fmt.Errorf("normalize bootstraps: %w", err)
	}
	cfg.Bootstraps = bootstraps
	generatedWireGuardPrivateKey := ""
	if cfg.DiscoveryEnabled && strings.TrimSpace(cfg.WireGuardPrivateKey) == "" {
		generatedWireGuardPrivateKey, err = utils.GenerateWireGuardPrivateKey()
		if err != nil {
			return nil, err
		}
		cfg.WireGuardPrivateKey = generatedWireGuardPrivateKey
	}
	wgConfig, err := wireguard.NormalizeConfig(rootHost, wireguard.Config{
		PrivateKey:   cfg.WireGuardPrivateKey,
		PublicKey:    cfg.WireGuardPublicKey,
		Endpoint:     cfg.WireGuardEndpoint,
		OverlayIPv4:  cfg.OverlayIPv4,
		OverlayCIDRs: cfg.OverlayCIDRs,
		ListenPort:   cfg.DiscoveryPort,
	})
	if err != nil {
		return nil, err
	}
	if generatedWireGuardPrivateKey != "" {
		log.Warn().
			Str("wireguard_public_key", wgConfig.PublicKey).
			Str("wireguard_private_key", generatedWireGuardPrivateKey).
			Msg("generated wireguard private key; set WIREGUARD_PRIVATE_KEY to preserve relay identity")
	}

	portMin, portMax := 0, 0
	if cfg.UDPPortCount > 0 {
		portMin = defaultUDPPortBase
		portMax = defaultUDPPortBase + cfg.UDPPortCount - 1
	}

	identity, generatedIdentity, err := utils.LoadOrCreateIdentity(cfg.IdentityPath, types.Identity{Name: rootHost})
	if err != nil {
		return nil, fmt.Errorf("load relay identity: %w", err)
	}
	if generatedIdentity {
		log.Warn().
			Str("identity_path", cfg.IdentityPath).
			Str("address", identity.Address).
			Msg("generated relay identity and saved it to disk")
	}

	policyRuntime := policy.NewRuntime()
	policyRuntime.SetUDPPolicy(cfg.UDPPortCount > 0, 0)
	registry := newLeaseRegistry(policyRuntime)
	ports := transport.NewPortAllocator(portMin, portMax, 5*time.Minute)

	s := &Server{
		cfg:               cfg,
		registry:          registry,
		ports:             ports,
		loadMgr:           policy.NewLoadManager(),
		weightMgr:         policy.NewWeightManager(),
		identity:          identity,
		wgConfig:          wgConfig,
		trustedProxyCIDRs: trustedProxyCIDRs,
		olsManager:        discovery.NewOLSManager(),
	}
	if cfg.I2PDiscoveryOnly {
		proxyURL := strings.TrimSpace(cfg.I2PProxyURL)
		if proxyURL != "" {
			parsedProxy, err := url.Parse(proxyURL)
			if err != nil {
				return nil, fmt.Errorf("parse i2p proxy url: %w", err)
			}
			s.discoveryClient = &http.Client{
				Transport: &http.Transport{
					Proxy: http.ProxyURL(parsedProxy),
				},
				Timeout: 15 * time.Second,
			}
		}
	}

	if cfg.DiscoveryEnabled {
		s.relaySet = discovery.NewRelaySet()
		_, err = s.relaySet.RegisterBootstrapRelayURLs(cfg.Bootstraps)
		if err != nil {
			return nil, err
		}
		s.ols = ols.New(identity.Key())
	}

	return s, nil
}
func (s *Server) Start(ctx context.Context, apiMux *http.ServeMux) error {
	if s.group != nil {
		return errors.New("server already started")
	}
	apiTLS, acmeManager, err := s.prepareAPITLS(ctx)
	if err != nil {
		return err
	}

	serverCtx, cancel := context.WithCancel(ctx)
	var listenConfig net.ListenConfig

	apiListener, err := listenConfig.Listen(serverCtx, "tcp", s.cfg.APIListenAddr)
	if err != nil {
		acmeManager.Stop()
		cancel()
		return fmt.Errorf("listen api: %w", err)
	}
	sniListener, err := listenConfig.Listen(serverCtx, "tcp", s.cfg.SNIListenAddr)
	if err != nil {
		acmeManager.Stop()
		_ = apiListener.Close()
		cancel()
		return fmt.Errorf("listen sni: %w", err)
	}

	group, groupCtx := errgroup.WithContext(serverCtx)
	wrappedAPIListener, apiServer, apiCloser, err := s.newAPIServer(apiListener, apiMux, apiTLS)
	if err != nil {
		acmeManager.Stop()
		_ = apiListener.Close()
		_ = sniListener.Close()
		cancel()
		return err
	}

	s.apiListener = wrappedAPIListener
	s.sniListener = sniListener
	s.apiServer = apiServer
	s.apiTLSClose = apiCloser
	s.acmeManager = acmeManager
	s.cancel = cancel
	s.group = group

	if s.wgConfig.PrivateKey != "" {
		if err := s.startOverlay(); err != nil {
			acmeManager.Stop()
			_ = apiServer.Close()
			_ = apiCloser.Close()
			_ = sniListener.Close()
			cancel()
			return err
		}
	}

	group.Go(s.runAPIServer)
	if s.overlay != nil {
		group.Go(s.overlay.Serve)
	}
	if s.interRelayListener != nil {
		group.Go(func() error { return s.runInterRelayProxyListener(groupCtx) })
	}
	group.Go(func() error { return s.runSNIListener(groupCtx) })
	group.Go(func() error { return s.runLeaseJanitor(groupCtx, 5*time.Second) })
	if s.cfg.DiscoveryEnabled {
		group.Go(func() error { return s.runRelayDiscoveryLoop(groupCtx) })
	}
	s.acmeManager.Start(serverCtx)

	if s.cfg.UDPPortCount > 0 {
		if err := s.startQUICTunnelListener(apiTLS); err != nil {
			log.Warn().Err(err).Msg("quic tunnel listener disabled")
		}
	}
	group.Go(func() error {
		<-groupCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.Shutdown(shutdownCtx)
	})

	logEvent := log.Info().
		Str("api_addr", utils.HostPortOrLoopback(s.apiListener.Addr().String())).
		Str("sni_addr", s.sniListener.Addr().String()).
		Str("root_host", s.identity.Name).
		Str("acme_dns_provider", s.cfg.ACME.DNSProvider).
		Bool("discovery_enabled", s.cfg.DiscoveryEnabled).
		Bool("wireguard_enabled", s.wgConfig.PrivateKey != "").
		Bool("udp_enabled", s.cfg.UDPPortCount > 0)
	if s.quicTunnel != nil {
		logEvent = logEvent.Str("internal_quic_tunnel_addr", s.quicTunnel.Addr().String())
	}
	logEvent.Msg("relay server started")

	return nil
}

func (s *Server) Wait() error {
	if s.group == nil {
		return nil
	}
	return s.group.Wait()
}

func (s *Server) Identity() types.Identity {
	if s == nil {
		return types.Identity{}
	}
	return s.identity.Copy()
}

func (s *Server) Shutdown(ctx context.Context) error {
	var shutdownErr error
	s.shutdownOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}

		for _, lease := range s.registry.CloseAll() {
			if lease != nil {
				if s.acmeManager != nil {
					deleteCtx, cancel := context.WithTimeout(ctx, defaultClaimTimeout)
					if err := s.acmeManager.DeleteENSGaslessHostname(deleteCtx, lease.Hostname); err != nil {
						log.Warn().
							Err(err).
							Str("hostname", lease.Hostname).
							Str("address", lease.Address).
							Msg("delete lease ens gasless txt during shutdown")
					}
					cancel()
				}
				lease.Close()
			}
		}

		if s.quicTunnel != nil {
			_ = s.quicTunnel.Close()
		}
		if s.sniListener != nil {
			if err := s.sniListener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				shutdownErr = err
			}
		}
		if s.interRelayListener != nil {
			if err := s.interRelayListener.Close(); err != nil && !errors.Is(err, net.ErrClosed) && shutdownErr == nil {
				shutdownErr = err
			}
		}
		if s.apiServer != nil {
			if err := s.apiServer.Shutdown(ctx); err != nil && shutdownErr == nil {
				shutdownErr = err
			}
		}
		if s.overlay != nil {
			if err := s.overlay.Shutdown(ctx); err != nil && shutdownErr == nil {
				shutdownErr = err
			}
		}
		if s.apiTLSClose != nil {
			_ = s.apiTLSClose.Close()
		}
		if s.acmeManager != nil {
			s.acmeManager.Stop()
		}
	})
	return shutdownErr
}

func (s *Server) PolicyRuntime() *policy.Runtime {
	if s == nil || s.registry == nil {
		return nil
	}
	return s.registry.policy
}

func (s *Server) PortalURL() string {
	if s == nil {
		return ""
	}
	return s.cfg.PortalURL
}

func (s *Server) LeaseSnapshots() []types.Lease {
	s.registry.mu.RLock()
	defer s.registry.mu.RUnlock()

	now := time.Now()
	records := make([]*leaseRecord, 0, len(s.registry.leasesByKey))
	for _, record := range s.registry.leasesByKey {
		records = append(records, record)
	}
	snapshots := make([]types.Lease, 0, len(records))
	for _, record := range records {
		if now.After(record.ExpiresAt) {
			continue
		}
		adminSnapshot := s.registry.AdminSnapshot(record)
		since := time.Duration(0)
		if !adminSnapshot.LastSeenAt.IsZero() {
			since = max(now.Sub(adminSnapshot.LastSeenAt), 0)
		}
		if adminSnapshot.IsBanned || adminSnapshot.IsDenied || !adminSnapshot.IsApproved || adminSnapshot.Metadata.Hide {
			continue
		}
		if adminSnapshot.Ready == 0 && since >= 3*time.Minute {
			continue
		}
		snapshots = append(snapshots, adminSnapshot.Lease)
	}
	return snapshots
}

func (s *Server) AdminLeaseSnapshots() []types.AdminLease {
	s.registry.mu.RLock()
	defer s.registry.mu.RUnlock()

	now := time.Now()
	records := make([]*leaseRecord, 0, len(s.registry.leasesByKey))
	for _, record := range s.registry.leasesByKey {
		records = append(records, record)
	}
	snapshots := make([]types.AdminLease, 0, len(records))
	for _, record := range records {
		if now.After(record.ExpiresAt) {
			continue
		}
		snapshots = append(snapshots, s.registry.AdminSnapshot(record))
	}
	return snapshots
}

func (s *Server) LeaseSnapshotByHostname(hostname string) (types.Lease, bool) {
	if s == nil || s.registry == nil {
		return types.Lease{}, false
	}

	record, ok := s.registry.Lookup(hostname)
	if !ok || record == nil || time.Now().After(record.ExpiresAt) {
		return types.Lease{}, false
	}
	return s.registry.Snapshot(record), true
}

func (s *Server) prepareAPITLS(ctx context.Context) (keyless.TLSMaterialConfig, *acme.Manager, error) {
	acmeCfg := s.cfg.ACME
	if baseDomain := utils.NormalizeHostname(acmeCfg.BaseDomain); baseDomain != "" && baseDomain != s.identity.Name {
		return keyless.TLSMaterialConfig{}, nil, fmt.Errorf("acme base domain %q does not match portal root host %q", acmeCfg.BaseDomain, s.identity.Name)
	}
	acmeCfg.BaseDomain = s.identity.Name
	if strings.TrimSpace(acmeCfg.ENSGaslessAddress) == "" {
		acmeCfg.ENSGaslessAddress = s.identity.Address
	}

	manager, err := acme.NewManager(acmeCfg)
	if err != nil {
		return keyless.TLSMaterialConfig{}, nil, fmt.Errorf("create acme manager: %w", err)
	}

	certPEM, keyPEM, err := manager.EnsureTLSMaterial(ctx)
	if err != nil {
		manager.Stop()
		return keyless.TLSMaterialConfig{}, nil, fmt.Errorf("ensure relay certificate: %w", err)
	}

	apiTLS := keyless.TLSMaterialConfig{
		CertPEM: certPEM,
		KeyPEM:  keyPEM,
	}
	if len(apiTLS.CertPEM) == 0 {
		manager.Stop()
		return keyless.TLSMaterialConfig{}, nil, errors.New("api tls certificate is required")
	}
	if len(apiTLS.KeyPEM) == 0 && apiTLS.Keyless == nil {
		manager.Stop()
		return keyless.TLSMaterialConfig{}, nil, errors.New("api tls key or keyless signer is required")
	}

	return apiTLS, manager, nil
}

func (s *Server) runSNIListener(ctx context.Context) error {
	for {
		conn, err := s.sniListener.Accept()
		switch {
		case err == nil:
			go func(conn net.Conn) {
				clientHello, wrappedConn, err := l4.InspectClientHello(conn, defaultClientHelloWait)
				if err != nil {
					if wrappedConn != nil {
						_ = wrappedConn.Close()
					} else {
						_ = conn.Close()
					}
					return
				}

				serverName := utils.NormalizeHostname(clientHello.ServerName)
				if serverName == "" {
					_ = wrappedConn.Close()
					return
				}

				// OLS load balancing: route to best target node if applicable.
				if s.ols != nil && s.relaySet != nil && s.overlay != nil {
					snapshot := s.relaySet.Snapshot()
					peers := &snapshotPeerDialer{snapshot: snapshot, overlay: s.overlay}
					if s.ols.RouteConn(ctx, wrappedConn, serverName, peers) {
						return
					}
				}

				if serverName == s.identity.Name {
					if s.apiListener == nil {
						_ = wrappedConn.Close()
						return
					}
					dialer := &net.Dialer{Timeout: 5 * time.Second}
					upstream, err := dialer.DialContext(ctx, "tcp", utils.HostPortOrLoopback(s.apiListener.Addr().String()))
					if err != nil {
						_ = wrappedConn.Close()
						return
					}
					s.BridgeConns(wrappedConn, upstream)
					return
				}

				record, ok := s.registry.Lookup(serverName)
				if !ok || record == nil || time.Now().After(record.ExpiresAt) || !s.registry.policy.IsIdentityRoutable(record.Key()) || record.stream == nil {
					_ = wrappedConn.Close()
					return
				}

				claimCtx, cancel := context.WithTimeout(ctx, defaultClaimTimeout)
				defer cancel()

				session, err := record.stream.Claim(claimCtx)
				if err != nil {
					_ = wrappedConn.Close()
					return
				}

				s.BridgeConns(wrappedConn, session)
			}(conn)
		case errors.Is(err, net.ErrClosed):
			return nil
		default:
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

func (s *Server) runLeaseJanitor(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("janitor interval must be positive")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for _, lease := range s.registry.cleanupExpired(time.Now()) {
				deleteCtx, cancel := context.WithTimeout(context.Background(), defaultClaimTimeout)
				err := s.acmeManager.DeleteENSGaslessHostname(deleteCtx, lease.Hostname)
				cancel()
				if err != nil {
					log.Warn().
						Err(err).
						Str("hostname", lease.Hostname).
						Str("address", lease.Address).
						Msg("delete expired lease ens gasless txt")
				}
				lease.Close()
			}
		}
	}
}

func (s *Server) startQUICTunnelListener(apiTLS keyless.TLSMaterialConfig) error {
	if len(apiTLS.KeyPEM) == 0 {
		return fmt.Errorf("quic tunnel requires api tls key")
	}
	tlsCert, err := tls.X509KeyPair(apiTLS.CertPEM, apiTLS.KeyPEM)
	if err != nil {
		return fmt.Errorf("parse quic tls keypair: %w", err)
	}

	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"portal-tunnel"},
		MinVersion:   tls.VersionTLS13,
	}
	quicConf := &quic.Config{
		EnableDatagrams:    true,
		KeepAlivePeriod:    15 * time.Second,
		MaxIdleTimeout:     60 * time.Second,
		MaxIncomingStreams: 16,
	}

	listener, err := quic.ListenAddr(s.cfg.QUICListenAddr, tlsConf, quicConf)
	if err != nil {
		return fmt.Errorf("listen quic: %w", err)
	}

	s.quicTunnel = listener
	s.group.Go(func() error { return s.runQUICTunnelListener(listener) })

	log.Info().
		Str("internal_quic_tunnel_addr", listener.Addr().String()).
		Msg("internal quic tunnel listener started")
	return nil
}

func (s *Server) runQUICTunnelListener(listener *quic.Listener) error {
	for {
		conn, err := listener.Accept(context.Background())
		if err != nil {
			if errors.Is(err, quic.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handleQUICTunnelConn(conn)
	}
}

func (s *Server) startOverlay() error {
	peerMux := http.NewServeMux()
	peerMux.HandleFunc(types.PathRoot, s.handleRoot)
	peerMux.HandleFunc(types.PathHealthz, s.handleHealthz)
	peerMux.HandleFunc(types.PathDiscovery, func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.DiscoveryEnabled {
			http.NotFound(w, r)
			return
		}
		s.handleRelayDiscovery(w, r)
	})

	overlay, err := wireguard.NewOverlay(s.wgConfig, peerMux)
	if err != nil {
		return fmt.Errorf("start wireguard overlay: %w", err)
	}

	interRelayListener, err := overlay.ListenTCP(7778)
	if err != nil {
		_ = overlay.Shutdown(context.Background())
		return fmt.Errorf("listen inter-relay proxy: %w", err)
	}

	if err := overlay.Sync(s.identity.Key(), s.relaySet.Snapshot()); err != nil {
		_ = interRelayListener.Close()
		_ = overlay.Shutdown(context.Background())
		return fmt.Errorf("sync wireguard peers: %w", err)
	}

	s.overlay = overlay
	s.interRelayListener = interRelayListener
	return nil
}

func (s *Server) runRelayDiscoveryLoop(ctx context.Context) error {
	ticker := time.NewTicker(types.DiscoveryPollInterval)
	defer ticker.Stop()
	var round uint64

	for {
		bootstraps := s.relaySet.BootstrapDescriptors()
		if s.olsManager != nil && len(bootstraps) > 1 {
			bootstraps = s.olsManager.OrderDescriptors(bootstraps, nil, round)
		}

		for _, bootstrap := range bootstraps {
			resp, err := discovery.DiscoverRelayDiscovery(ctx, bootstrap.APIHTTPSAddr, nil, s.discoveryClient)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				s.relaySet.RecordBootstrapDiscoveryFailure(bootstrap.APIHTTPSAddr, err, time.Now().UTC())
				continue
			}

			now := time.Now().UTC()
			var relaySetChanged bool
			var warnErr error
			_, relaySetChanged, _, warnErr, err = s.relaySet.ApplyRelayDiscoveryResponse(bootstrap.Identity, bootstrap.APIHTTPSAddr, resp, now)
			if relaySetChanged {
				if s.ols != nil {
					s.ols.OnRelaySetChanged(s.localLoad(), s.relaySet.Snapshot())
				}
				if s.overlay != nil {
					if syncErr := s.overlay.Sync(s.identity.Key(), s.relaySet.Snapshot()); syncErr != nil {
						if warnErr == nil {
							warnErr = syncErr
						}
					}
				}
			}
			if err != nil {
				s.relaySet.MarkRelayFailure(bootstrap.APIHTTPSAddr, time.Now().UTC())
				log.Warn().
					Err(err).
					Str("relay", bootstrap.APIHTTPSAddr).
					Msg("bootstrap relay discovery failed")
				continue
			}

			if warnErr != nil {
				log.Warn().
					Err(warnErr).
					Str("relay", bootstrap.APIHTTPSAddr).
					Msg("bootstrap relay discovery completed with warnings")
			}
		}
		if ctx.Err() != nil {
			return nil
		}

		if s.overlay != nil {
			overlayClient := s.overlay.Client()
			syncableRelays := s.relaySet.SyncableDescriptors()
			if s.olsManager != nil && len(syncableRelays) > 1 {
				loadByURL := map[string]float64{}
				snapshot := s.relaySet.Snapshot()
				for _, relay := range syncableRelays {
					loadByURL[relay.APIHTTPSAddr] = float64(snapshot[relay.Key()].ConsecutiveFailures)
				}
				syncableRelays = s.olsManager.OrderDescriptors(syncableRelays, loadByURL, round)
			}

			for _, relay := range syncableRelays {
				var failureErr error

				if err := discovery.RequireOverlayRelayDescriptor(relay); err != nil {
					failureErr = err
				} else {
					discoverURL := "http://" + net.JoinHostPort(relay.OverlayIPv4, fmt.Sprintf("%d", wireguard.DefaultPeerAPIHTTPPort))
					resp, err := discovery.DiscoverRelayDiscovery(ctx, discoverURL, nil, overlayClient)
					if err != nil {
						if ctx.Err() != nil {
							return nil
						}
						failureErr = err
					} else {
						now := time.Now().UTC()
						var relaySetChanged bool
						var warnErr error
						var snapshot map[string]types.RelayState
						_, relaySetChanged, _, warnErr, err = s.relaySet.ApplyOverlayRelayDiscoveryResponse(relay.Identity, relay.APIHTTPSAddr, resp, now)
						if relaySetChanged {
							if s.ols != nil {
								s.ols.OnRelaySetChanged(s.localLoad(), s.relaySet.Snapshot())
							}
							snapshot = s.relaySet.Snapshot()
							if syncErr := s.overlay.Sync(s.identity.Key(), snapshot); syncErr != nil {
								if warnErr == nil {
									warnErr = syncErr
								}
							}
						}
						if err != nil {
							failureErr = err
						} else {
							if warnErr != nil {
								log.Warn().
									Err(warnErr).
									Str("relay", relay.APIHTTPSAddr).
									Msg("overlay relay discovery completed with warnings")
								continue
							}

							continue
						}
					}
				}
				expired, expireReason, consecutiveFailures := s.relaySet.RecordDiscoveryFailure(relay.Identity, relay.APIHTTPSAddr, failureErr, defaultWGRecoveryFailures, time.Now().UTC())
				if expired {
					if syncErr := s.overlay.Sync(s.identity.Key(), s.relaySet.Snapshot()); syncErr != nil && failureErr == nil {
						failureErr = syncErr
					}
				}

				event := log.Warn().
					Err(failureErr).
					Str("relay", relay.APIHTTPSAddr)
				if expired {
					event = event.
						Bool("expired", true).
						Str("reason", expireReason)
					if consecutiveFailures > 0 {
						event = event.Int("consecutive_failures", consecutiveFailures)
					}
				}
				event.Msg("overlay relay discovery failed")
			}
		}
		if ctx.Err() != nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			round++
		}
	}
}

func (s *Server) localLoad() policy.NodeLoad {
	if s == nil || s.loadMgr == nil {
		return policy.NodeLoad{}
	}
	load := s.loadMgr.Snapshot()
	if s.weightMgr != nil {
		w := s.weightMgr.Collect()
		load.AvgLatencyMs = w.AvgLatencyMs
	}
	return load
}

func (s *Server) runInterRelayProxyListener(ctx context.Context) error {
	for {
		conn, err := s.interRelayListener.Accept()
		switch {
		case err == nil:
			go func(conn net.Conn) {
				if s.ols == nil || s.relaySet == nil || s.overlay == nil {
					_ = conn.Close()
					return
				}
				snapshot := s.relaySet.Snapshot()
				peers := &snapshotPeerDialer{snapshot: snapshot, overlay: s.overlay}
				s.ols.ServeInterRelayConn(ctx, conn, peers, func(serverName string, c net.Conn) {
					s.serveLocalConn(ctx, serverName, c)
				})
			}(conn)
		case errors.Is(err, net.ErrClosed):
			return nil
		default:
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

func (s *Server) serveLocalConn(ctx context.Context, serverName string, conn net.Conn) {
	if serverName == s.identity.Name {
		if s.apiListener == nil {
			_ = conn.Close()
			return
		}
		dialer := &net.Dialer{Timeout: 5 * time.Second}
		upstream, err := dialer.DialContext(ctx, "tcp", utils.HostPortOrLoopback(s.apiListener.Addr().String()))
		if err != nil {
			_ = conn.Close()
			return
		}
		s.BridgeConns(conn, upstream)
		return
	}

	record, ok := s.registry.Lookup(serverName)
	if !ok || record == nil || time.Now().After(record.ExpiresAt) || !s.registry.policy.IsIdentityRoutable(record.Key()) || record.stream == nil {
		_ = conn.Close()
		return
	}

	claimCtx, cancel := context.WithTimeout(ctx, defaultClaimTimeout)
	defer cancel()

	session, err := record.stream.Claim(claimCtx)
	if err != nil {
		_ = conn.Close()
		return
	}

	s.BridgeConns(conn, session)
}

type snapshotPeerDialer struct {
	snapshot map[string]types.RelayState
	overlay  *wireguard.Overlay
}

func (d *snapshotPeerDialer) PeerAddr(nodeID string) (string, bool) {
	state, ok := d.snapshot[nodeID]
	if !ok || state.Expired {
		return "", false
	}
	if !state.Descriptor.SupportsOverlayPeer || strings.TrimSpace(state.Descriptor.OverlayIPv4) == "" {
		return "", false
	}
	return net.JoinHostPort(state.Descriptor.OverlayIPv4, "7778"), true
}

func (d *snapshotPeerDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d == nil || d.overlay == nil {
		return nil, errors.New("overlay is not initialized")
	}
	return d.overlay.DialContext(ctx, network, address)
}

func (s *Server) BridgeConns(left, right net.Conn) {
	s.loadMgr.RecordConnStart()
	defer s.loadMgr.RecordConnEnd()

	defer left.Close()
	defer right.Close()

	var group errgroup.Group
	group.Go(func() error {
		n, err := io.Copy(right, left)
		s.loadMgr.RecordBytesIn(n)
		closeWrite(right)
		return err
	})
	group.Go(func() error {
		n, err := io.Copy(left, right)
		s.loadMgr.RecordBytesOut(n)
		closeWrite(left)
		return err
	})
	_ = group.Wait()
}

func closeWrite(conn net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	if cw, ok := conn.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
}
