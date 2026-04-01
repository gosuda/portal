package portal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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
	OwnerPrivateKey     string
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
}

type Server struct {
	sniListener       net.Listener
	apiListener       net.Listener
	apiServer         *http.Server
	apiTLSClose       io.Closer
	acmeManager       *acme.Manager
	quicTunnel        *quic.Listener
	overlay           *wireguard.Overlay
	cancel            context.CancelFunc
	group             *errgroup.Group
	registry          *leaseRegistry
	ports             *transport.PortAllocator
	ownerIdentity     utils.Secp256k1Identity
	wgConfig          wireguard.Config
	cfg               ServerConfig
	rootHost          string
	trustedProxyCIDRs []*net.IPNet
	relaySet          *discovery.RelaySet
	shutdownOnce      sync.Once
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

	ownerPrivateKey := strings.TrimSpace(cfg.OwnerPrivateKey)
	ownerIdentity, err := utils.ResolveSecp256k1Identity(ownerPrivateKey)
	if err != nil {
		if ownerPrivateKey == "" {
			return nil, fmt.Errorf("generate relay owner private key: %w", err)
		}
		return nil, fmt.Errorf("resolve owner identity: %w", err)
	}
	if ownerPrivateKey == "" {
		log.Warn().
			Str("owner_address", ownerIdentity.Address).
			Str("owner_private_key", ownerIdentity.PrivateKey).
			Msg("generated relay owner private key; set OWNER_PRIVATE_KEY unique identity")
	}

	policy := policy.NewRuntime()
	policy.SetUDPPolicy(cfg.UDPPortCount > 0, 0)
	registry := newLeaseRegistry(policy)
	ports := transport.NewPortAllocator(portMin, portMax, 5*time.Minute)

	s := &Server{
		cfg:               cfg,
		rootHost:          rootHost,
		registry:          registry,
		ports:             ports,
		ownerIdentity:     ownerIdentity,
		wgConfig:          wgConfig,
		trustedProxyCIDRs: trustedProxyCIDRs,
	}

	if cfg.DiscoveryEnabled {
		s.relaySet = discovery.NewRelaySet()
		_, err = s.relaySet.RegisterBootstrapRelayURLs(cfg.Bootstraps, time.Now().UTC())
		if err != nil {
			return nil, err
		}
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
	group.Go(func() error { return s.runSNIListener(groupCtx) })
	group.Go(func() error { return s.registry.RunJanitor(groupCtx, 5*time.Second) })
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
		Str("root_host", s.rootHost).
		Str("acme_dns_provider", s.cfg.ACME.DNSProvider).
		Bool("discovery_enabled", s.cfg.DiscoveryEnabled).
		Bool("wireguard_enabled", strings.TrimSpace(s.wgConfig.PrivateKey) != "").
		Bool("udp_enabled", s.cfg.UDPPortCount > 0).
		Bool("acme_enabled", !strings.HasSuffix(s.rootHost, "localhost") && s.rootHost != "127.0.0.1" && s.rootHost != "::1")
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

func (s *Server) Shutdown(ctx context.Context) error {
	var shutdownErr error
	s.shutdownOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}

		for _, lease := range s.registry.CloseAll() {
			if lease != nil {
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

	records := make([]*leaseRecord, 0, len(s.registry.leaseByID))
	for _, record := range s.registry.leaseByID {
		records = append(records, record)
	}
	snapshots := make([]types.Lease, 0, len(records))
	for _, record := range records {
		snapshots = append(snapshots, s.registry.Snapshot(record))
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
	if baseDomain := utils.NormalizeHostname(acmeCfg.BaseDomain); baseDomain != "" && baseDomain != s.rootHost {
		return keyless.TLSMaterialConfig{}, nil, fmt.Errorf("acme base domain %q does not match portal root host %q", acmeCfg.BaseDomain, s.rootHost)
	}
	acmeCfg.BaseDomain = s.rootHost

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

				if serverName == s.rootHost {
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
					BridgeConns(wrappedConn, upstream)
					return
				}

				record, ok := s.registry.Lookup(serverName)
				if !ok || record == nil || time.Now().After(record.ExpiresAt) || !s.registry.policy.IsLeaseRoutable(record.ID) || record.stream == nil {
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

				BridgeConns(wrappedConn, session)
			}(conn)
		case errors.Is(err, net.ErrClosed):
			return nil
		default:
			return err
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

	if err := overlay.Sync(s.cfg.PortalURL, s.relaySet.Snapshot()); err != nil {
		_ = overlay.Shutdown(context.Background())
		return fmt.Errorf("sync wireguard peers: %w", err)
	}

	s.overlay = overlay
	return nil
}

func (s *Server) runRelayDiscoveryLoop(ctx context.Context) error {
	ticker := time.NewTicker(types.DiscoveryPollInterval)
	defer ticker.Stop()

	for {
		bootstraps := s.relaySet.BootstrapDescriptors()

		for _, bootstrap := range bootstraps {
			resp, err := discovery.DiscoverRelayDiscovery(ctx, bootstrap.APIHTTPSAddr, nil, nil)
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
			_, relaySetChanged, _, warnErr, err = s.relaySet.ApplyRelayDiscoveryResponse(bootstrap.RelayID, bootstrap.APIHTTPSAddr, resp, now)
			if relaySetChanged && s.overlay != nil {
				if syncErr := s.overlay.Sync(s.cfg.PortalURL, s.relaySet.Snapshot()); syncErr != nil {
					if warnErr == nil {
						warnErr = syncErr
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
						_, relaySetChanged, _, warnErr, err = s.relaySet.ApplyOverlayRelayDiscoveryResponse(relay.RelayID, relay.APIHTTPSAddr, resp, now)
						if relaySetChanged {
							snapshot = s.relaySet.Snapshot()
							if syncErr := s.overlay.Sync(s.cfg.PortalURL, snapshot); syncErr != nil {
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
				expired, expireReason, consecutiveFailures := s.relaySet.RecordDiscoveryFailure(relay.RelayID, relay.APIHTTPSAddr, failureErr, defaultWGRecoveryFailures, time.Now().UTC())
				if expired {
					if syncErr := s.overlay.Sync(s.cfg.PortalURL, s.relaySet.Snapshot()); syncErr != nil && failureErr == nil {
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
		}
	}
}

func BridgeConns(left, right net.Conn) {
	defer left.Close()
	defer right.Close()

	var group errgroup.Group
	group.Go(func() error {
		_, err := io.Copy(right, left)
		closeWrite(right)
		return err
	})
	group.Go(func() error {
		_, err := io.Copy(left, right)
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
