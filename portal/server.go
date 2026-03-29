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
	defaultDiscoveryInterval  = 30 * time.Second
	defaultIdleKeepalive      = 15 * time.Second
	defaultReadyQueueLimit    = 8
	defaultClientHelloWait    = 2 * time.Second
	defaultControlBodyLimit   = 4 << 20
	defaultUDPPortBase        = 50000
	defaultWGRecoveryFailures = 3
)

type ServerConfig struct {
	PortalURL             string
	OwnerPrivateKey       string
	Bootstraps            []string
	WireGuardPrivateKey   string
	DiscoveryPort         int
	WireGuardPublicKey    string
	WireGuardEndpoint     string
	OverlayIPv4           string
	OverlayCIDRs          []string
	ACME                  acme.Config
	APIPort               int
	SNIPort               int
	APIListenAddr         string
	SNIListenAddr         string
	QUICListenAddr        string
	TrustedProxyCIDRs     string
	LeaseTTL              time.Duration
	ClaimTimeout          time.Duration
	IdleKeepaliveInterval time.Duration
	ReadyQueueLimit       int
	ClientHelloTimeout    time.Duration
	TrustProxyHeaders     bool
	DiscoveryEnabled      bool
	UDPPortCount          int
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
	peerRegistry      *peerRegistry
	shutdownOnce      sync.Once
}

func NewServer(cfg ServerConfig) (*Server, error) {
	cfg.PortalURL = strings.TrimSuffix(strings.TrimSpace(cfg.PortalURL), "/")
	cfg.APIPort = utils.IntOrDefault(cfg.APIPort, 4017)
	cfg.SNIPort = utils.IntOrDefault(cfg.SNIPort, 443)
	cfg.APIListenAddr = utils.StringOrDefault(cfg.APIListenAddr, fmt.Sprintf(":%d", cfg.APIPort))
	cfg.SNIListenAddr = utils.StringOrDefault(cfg.SNIListenAddr, fmt.Sprintf(":%d", cfg.SNIPort))
	cfg.LeaseTTL = utils.DurationOrDefault(cfg.LeaseTTL, defaultLeaseTTL)
	cfg.ClaimTimeout = utils.DurationOrDefault(cfg.ClaimTimeout, defaultClaimTimeout)
	cfg.IdleKeepaliveInterval = utils.DurationOrDefault(cfg.IdleKeepaliveInterval, defaultIdleKeepalive)
	cfg.ReadyQueueLimit = utils.IntOrDefault(cfg.ReadyQueueLimit, defaultReadyQueueLimit)
	cfg.ClientHelloTimeout = utils.DurationOrDefault(cfg.ClientHelloTimeout, defaultClientHelloWait)
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
	if cfg.DiscoveryEnabled && strings.TrimSpace(wgConfig.PrivateKey) == "" {
		return nil, errors.New("wireguard private key is required when discovery is enabled")
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
	cfg.OwnerPrivateKey = ""

	runtime := policy.NewRuntime()
	runtime.SetUDPPolicy(cfg.UDPPortCount > 0, 0)
	registry := newLeaseRegistry(runtime)
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

	// Tear down all lease resources when leases expire via TTL janitor.
	registry.onExpired = func(record *leaseRecord) {
		if record != nil {
			record.Close()
		}
	}

	if cfg.DiscoveryEnabled {
		s.peerRegistry = newPeerRegistry()
		if _, err := s.peerRegistry.registerBootstrapURLs(cfg.Bootstraps); err != nil {
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
		var snapshot map[string]types.PeerState
		if s.peerRegistry != nil {
			snapshot = s.peerRegistry.snapshot()
		}
		peerMux := http.NewServeMux()
		peerMux.HandleFunc(types.PathRoot, s.handleRoot)
		peerMux.HandleFunc(types.PathHealthz, s.handleHealthz)
		peerMux.HandleFunc(types.PathDiscovery, func(w http.ResponseWriter, r *http.Request) {
			if !s.DiscoveryEnabled() {
				http.NotFound(w, r)
				return
			}
			discovery.ServeHTTP(w, r, s.discover)
		})
		overlay, err := wireguard.NewOverlay(s.wgConfig, peerMux)
		if err != nil {
			acmeManager.Stop()
			_ = apiServer.Close()
			_ = apiCloser.Close()
			_ = sniListener.Close()
			cancel()
			return fmt.Errorf("start wireguard overlay: %w", err)
		}
		if err := overlay.Sync(s.cfg.PortalURL, snapshot); err != nil {
			acmeManager.Stop()
			_ = apiServer.Close()
			_ = apiCloser.Close()
			_ = sniListener.Close()
			_ = overlay.Shutdown(context.Background())
			cancel()
			return fmt.Errorf("sync wireguard peers: %w", err)
		}
		s.overlay = overlay
	}

	group.Go(s.runAPIServer)
	if s.overlay != nil {
		group.Go(s.overlay.Serve)
	}
	group.Go(func() error { return s.runSNIListener(groupCtx) })
	group.Go(func() error { return s.registry.RunJanitor(groupCtx, 5*time.Second) })
	if s.DiscoveryEnabled() {
		group.Go(func() error { return s.runDiscoveryLoop(groupCtx) })
	}
	group.Go(func() error {
		<-groupCtx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.Shutdown(shutdownCtx)
	})
	s.acmeManager.Start(serverCtx)

	if s.cfg.UDPPortCount > 0 {
		if err := s.startQUICTunnelListener(apiTLS); err != nil {
			log.Warn().Err(err).Msg("quic tunnel listener disabled")
		}
	}

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

func (s *Server) APIAddr() string {
	if s.apiListener == nil {
		return ""
	}
	return s.apiListener.Addr().String()
}

func (s *Server) SNIAddr() string {
	if s.sniListener == nil {
		return ""
	}
	return s.sniListener.Addr().String()
}

func (s *Server) QUICTunnelAddr() string {
	if s.quicTunnel == nil {
		return ""
	}
	return s.quicTunnel.Addr().String()
}

func (s *Server) DiscoveryEnabled() bool {
	return s != nil && s.cfg.DiscoveryEnabled
}

func (s *Server) PortalURL() string {
	if s == nil {
		return ""
	}
	return s.cfg.PortalURL
}

func (s *Server) OwnerAddress() string {
	if s == nil {
		return ""
	}
	return s.ownerIdentity.Address
}

func (s *Server) RootHost() string {
	if s == nil {
		return ""
	}
	return s.rootHost
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
				clientHello, wrappedConn, err := l4.InspectClientHello(conn, s.cfg.ClientHelloTimeout)
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

				claimCtx, cancel := context.WithTimeout(ctx, s.cfg.ClaimTimeout)
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

func (s *Server) applyDiscoveryResponse(targetRelayID, targetURL string, resp types.DiscoverResponse, requireSelfOverlay bool) (updated bool, addedHintCount int, warnErr error, err error) {
	if strings.TrimSpace(targetRelayID) == "" {
		return false, 0, nil, errors.New("target relay id is required")
	}

	now := time.Now().UTC()
	selfDescriptor, peerDescriptors, warnErr := discovery.ValidateResponse(resp, now)
	if selfDescriptor.RelayID == "" {
		return false, 0, nil, errors.Join(warnErr, errors.New("discover response is missing self descriptor"))
	}
	if selfDescriptor.RelayID != targetRelayID {
		return false, 0, nil, errors.Join(warnErr, errors.New("discover response relay_id mismatch"))
	}
	if requireSelfOverlay {
		if err := requireOverlayPeerDescriptor(selfDescriptor); err != nil {
			return false, 0, nil, errors.Join(warnErr, err)
		}
	}

	filteredPeerDescriptors := make([]types.RelayDescriptor, 0, len(peerDescriptors))

	for _, peerDescriptor := range peerDescriptors {
		if err := requireOverlayPeerDescriptor(peerDescriptor); err != nil {
			warnErr = errors.Join(warnErr, fmt.Errorf("record hint %q: %w", peerDescriptor.RelayID, err))
			continue
		}
		filteredPeerDescriptors = append(filteredPeerDescriptors, peerDescriptor)
	}

	result, err := s.peerRegistry.registerDiscoveredPeers(targetRelayID, targetURL, selfDescriptor, filteredPeerDescriptors)
	if err != nil {
		return false, 0, nil, errors.Join(warnErr, err)
	}
	updated = result.updated
	addedHintCount = result.addedHintCount

	if result.peerSetChanged && s.overlay != nil {
		if err := s.overlay.Sync(s.cfg.PortalURL, s.peerRegistry.snapshot()); err != nil {
			warnErr = errors.Join(warnErr, err)
		}
	}

	return updated, addedHintCount, warnErr, nil
}

func (s *Server) runBootstrapDiscoveryPass(ctx context.Context) {
	for _, bootstrap := range s.peerRegistry.bootstrapPeers() {
		resp, err := discovery.Discover(ctx, bootstrap.APIHTTPSAddr, types.DiscoverRequest{}, nil, nil)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.peerRegistry.fail(bootstrap.RelayID)
			log.Warn().
				Err(err).
				Str("peer", bootstrap.APIHTTPSAddr).
				Msg("bootstrap discovery failed")
			continue
		}

		updated, addedHintCount, warnErr, err := s.applyDiscoveryResponse(bootstrap.RelayID, bootstrap.APIHTTPSAddr, resp, false)
		if err != nil {
			s.peerRegistry.fail(bootstrap.RelayID)
			log.Warn().
				Err(err).
				Str("peer", bootstrap.APIHTTPSAddr).
				Msg("bootstrap discovery failed")
			continue
		}

		if updated || warnErr != nil {
			event := log.Info()
			if warnErr != nil {
				event = log.Warn().Err(warnErr)
			}
			event = event.
				Str("peer", bootstrap.APIHTTPSAddr).
				Int("bootstrap_count", len(s.peerRegistry.bootstrapPeers())).
				Int("peer_count", len(s.peerRegistry.syncablePeers())).
				Int("advertised_count", len(s.peerRegistry.advertisedPeers()))
			if addedHintCount > 0 {
				event = event.Int("added_hint_count", addedHintCount)
			}
			if warnErr != nil {
				event.Msg("bootstrap discovery completed with warnings")
			} else {
				event.Msg("bootstrap discovery updated")
			}
		}
	}
}

func (s *Server) runOverlayPeerDiscoveryPass(ctx context.Context) {
	if s.overlay == nil {
		return
	}
	overlayClient := s.overlay.Client()
	if overlayClient == nil {
		return
	}

	for _, peer := range s.peerRegistry.syncablePeers() {
		var failureErr error

		if err := requireOverlayPeerDescriptor(peer); err != nil {
			failureErr = err
		} else {
			discoverURL := "http://" + net.JoinHostPort(peer.OverlayIPv4, fmt.Sprintf("%d", wireguard.DefaultPeerAPIHTTPPort))
			resp, err := discovery.Discover(ctx, discoverURL, types.DiscoverRequest{}, nil, overlayClient)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				failureErr = err
			} else {
				updated, addedHintCount, warnErr, err := s.applyDiscoveryResponse(peer.RelayID, peer.APIHTTPSAddr, resp, true)
				if err != nil {
					failureErr = err
				} else {
					if warnErr != nil {
						log.Warn().
							Err(warnErr).
							Str("peer", peer.APIHTTPSAddr).
							Int("bootstrap_count", len(s.peerRegistry.bootstrapPeers())).
							Int("peer_count", len(s.peerRegistry.syncablePeers())).
							Int("advertised_count", len(s.peerRegistry.advertisedPeers())).
							Int("added_hint_count", addedHintCount).
							Msg("overlay peer discovery completed with warnings")
						continue
					}

					if updated {
						event := log.Info().
							Str("peer", peer.APIHTTPSAddr).
							Int("bootstrap_count", len(s.peerRegistry.bootstrapPeers())).
							Int("peer_count", len(s.peerRegistry.syncablePeers())).
							Int("advertised_count", len(s.peerRegistry.advertisedPeers()))
						if addedHintCount > 0 {
							event = event.Int("added_hint_count", addedHintCount)
						}
						event.Msg("overlay peer discovery updated")
					}
					continue
				}
			}
		}

		result := s.peerRegistry.recordFailure(peer.RelayID, failureErr, defaultWGRecoveryFailures)
		if result.expired && s.overlay != nil {
			failureErr = errors.Join(failureErr, s.overlay.Sync(s.cfg.PortalURL, s.peerRegistry.snapshot()))
		}

		event := log.Warn().
			Err(failureErr).
			Str("peer", peer.APIHTTPSAddr)
		if result.expired {
			event = event.
				Bool("expired", true).
				Str("reason", result.expireReason)
			if result.consecutiveFailures > 0 {
				event = event.Int("consecutive_failures", result.consecutiveFailures)
			}
		}
		event.Msg("overlay peer discovery failed")
	}
}

func (s *Server) runDiscoveryLoop(ctx context.Context) error {
	ticker := time.NewTicker(defaultDiscoveryInterval)
	defer ticker.Stop()

	for {
		s.runBootstrapDiscoveryPass(ctx)
		if ctx.Err() != nil {
			return nil
		}
		s.runOverlayPeerDiscoveryPass(ctx)
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
