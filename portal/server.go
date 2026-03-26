package portal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
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
	wgPeerListener    net.Listener
	wgPeerServer      *http.Server
	apiTLSClose       io.Closer
	acmeManager       *acme.Manager
	quicTunnel        *quic.Listener
	wgRuntime         *wireguard.Runtime
	cancel            context.CancelFunc
	group             *errgroup.Group
	registry          *leaseRegistry
	ports             *transport.PortAllocator
	ownerIdentity     discovery.Identity
	cfg               ServerConfig
	rootHost          string
	trustedProxyCIDRs []*net.IPNet
	discoveryCache    *discovery.Cache
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
	wireGuardConfigured := strings.TrimSpace(cfg.WireGuardPrivateKey) != "" ||
		strings.TrimSpace(cfg.WireGuardPublicKey) != "" ||
		strings.TrimSpace(cfg.WireGuardEndpoint) != "" ||
		strings.TrimSpace(cfg.OverlayIPv4) != "" ||
		len(cfg.OverlayCIDRs) > 0
	if wireGuardConfigured {
		if strings.TrimSpace(cfg.WireGuardPrivateKey) == "" {
			return nil, errors.New("wireguard private key is required when relay overlay is enabled")
		}
		cfg.WireGuardPrivateKey, err = utils.NormalizeWireGuardPrivateKey(cfg.WireGuardPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("normalize wireguard private key: %w", err)
		}
		derivedPublicKey, err := utils.WireGuardPublicKeyFromPrivate(cfg.WireGuardPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("derive wireguard public key: %w", err)
		}
		if configuredPublicKey := strings.TrimSpace(cfg.WireGuardPublicKey); configuredPublicKey != "" && configuredPublicKey != derivedPublicKey {
			return nil, errors.New("wireguard public key does not match private key")
		}
		cfg.WireGuardPublicKey = derivedPublicKey
		cfg.DiscoveryPort = utils.IntOrDefault(cfg.DiscoveryPort, wireguard.DefaultListenPort)
		if len(cfg.OverlayCIDRs) > 0 {
			cfg.OverlayCIDRs, err = discovery.NormalizeOverlayCIDRs(cfg.OverlayCIDRs)
			if err != nil {
				return nil, fmt.Errorf("normalize overlay cidrs: %w", err)
			}
		}
		if strings.TrimSpace(cfg.WireGuardEndpoint) == "" {
			cfg.WireGuardEndpoint = net.JoinHostPort(rootHost, fmt.Sprintf("%d", cfg.DiscoveryPort))
		}
		if strings.TrimSpace(cfg.OverlayIPv4) == "" {
			cfg.OverlayIPv4, err = utils.DeriveWireGuardOverlayIPv4(cfg.WireGuardPublicKey)
			if err != nil {
				return nil, fmt.Errorf("derive overlay ipv4: %w", err)
			}
		}
		if err := discovery.ValidateWireGuardEndpoint(cfg.WireGuardEndpoint); err != nil {
			return nil, err
		}
		if err := discovery.ValidateOverlayIPv4(cfg.OverlayIPv4); err != nil {
			return nil, err
		}
	}

	portMin, portMax := 0, 0
	if cfg.UDPPortCount > 0 {
		portMin = defaultUDPPortBase
		portMax = defaultUDPPortBase + cfg.UDPPortCount - 1
	}

	ownerPrivateKey := strings.TrimSpace(cfg.OwnerPrivateKey)
	ownerIdentity, err := discovery.ResolveIdentity(ownerPrivateKey)
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
		trustedProxyCIDRs: trustedProxyCIDRs,
	}

	// Tear down all lease resources when leases expire via TTL janitor.
	registry.onExpired = func(record *leaseRecord) {
		s.closeLease(record)
	}

	if cfg.DiscoveryEnabled {
		s.discoveryCache = discovery.NewCache()
		if _, err := s.discoveryCache.UpsertSeedURLs(cfg.Bootstraps); err != nil {
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

	if s.wireGuardPeerPlaneEnabled() {
		if err := s.startWireGuardPeerPlane(); err != nil {
			acmeManager.Stop()
			_ = apiServer.Close()
			_ = apiCloser.Close()
			_ = sniListener.Close()
			cancel()
			return fmt.Errorf("start wireguard peer plane: %w", err)
		}
	}

	group.Go(s.runAPIServer)
	if s.wgPeerServer != nil {
		group.Go(s.runWireGuardPeerAPIServer)
	}
	group.Go(func() error { return s.runSNIListener(groupCtx) })
	group.Go(func() error { return s.registry.RunJanitor(groupCtx, 5*time.Second) })
	if s.DiscoveryEnabled() {
		group.Go(func() error { return s.runDiscoveryLoop(groupCtx) })
	}
	group.Go(func() error { return s.watchContext(groupCtx) })
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
			s.closeLease(lease)
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
		if s.wgPeerServer != nil {
			if err := s.wgPeerServer.Shutdown(ctx); err != nil && shutdownErr == nil && !errors.Is(err, http.ErrServerClosed) {
				shutdownErr = err
			}
		}
		if s.wgRuntime != nil {
			_ = s.wgRuntime.Close()
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

func (s *Server) wireGuardPeerPlaneEnabled() bool {
	if s == nil {
		return false
	}
	return strings.TrimSpace(s.cfg.WireGuardPrivateKey) != "" &&
		strings.TrimSpace(s.cfg.WireGuardPublicKey) != "" &&
		strings.TrimSpace(s.cfg.WireGuardEndpoint) != "" &&
		strings.TrimSpace(s.cfg.OverlayIPv4) != ""
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

func (s *Server) startWireGuardPeerPlane() error {
	runtime, err := wireguard.NewRuntime(wireguard.RuntimeConfig{
		PrivateKey:  s.cfg.WireGuardPrivateKey,
		Endpoint:    s.cfg.WireGuardEndpoint,
		OverlayIPv4: s.cfg.OverlayIPv4,
	})
	if err != nil {
		return err
	}

	listener, err := runtime.ListenTCP(wireguard.DefaultPeerAPIHTTPPort)
	if err != nil {
		_ = runtime.Close()
		return fmt.Errorf("listen peer api: %w", err)
	}

	server := &http.Server{
		Handler:           s.peerAPIHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	s.wgRuntime = runtime
	s.wgPeerListener = listener
	s.wgPeerServer = server

	if err := s.syncWireGuardPeers(); err != nil {
		_ = server.Close()
		_ = runtime.Close()
		s.wgRuntime = nil
		s.wgPeerListener = nil
		s.wgPeerServer = nil
		return fmt.Errorf("seed wireguard peers: %w", err)
	}
	return nil
}

func (s *Server) runWireGuardPeerAPIServer() error {
	if s == nil || s.wgPeerServer == nil || s.wgPeerListener == nil {
		return nil
	}

	err := s.wgPeerServer.Serve(s.wgPeerListener)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *Server) peerAPIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(types.PathRoot, s.handleRoot)
	mux.HandleFunc(types.PathHealthz, s.handleHealthz)
	mux.HandleFunc(types.PathDiscovery, func(w http.ResponseWriter, r *http.Request) {
		if !s.DiscoveryEnabled() {
			http.NotFound(w, r)
			return
		}
		discovery.ServeHTTP(w, r, s.discover)
	})
	return mux
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
	if err := validateAPITLS(apiTLS); err != nil {
		manager.Stop()
		return keyless.TLSMaterialConfig{}, nil, err
	}

	return apiTLS, manager, nil
}

func validateAPITLS(apiTLS keyless.TLSMaterialConfig) error {
	if len(apiTLS.CertPEM) == 0 {
		return errors.New("api tls certificate is required")
	}
	if len(apiTLS.KeyPEM) == 0 && apiTLS.Keyless == nil {
		return errors.New("api tls key or keyless signer is required")
	}
	return nil
}

func (s *Server) runSNIListener(ctx context.Context) error {
	for {
		conn, err := s.sniListener.Accept()
		switch {
		case err == nil:
			go s.handleSNIConn(ctx, conn)
		case errors.Is(err, net.ErrClosed):
			return nil
		default:
			return err
		}
	}
}

func (s *Server) handleSNIConn(ctx context.Context, conn net.Conn) {
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
		s.bridgeToAPI(ctx, wrappedConn)
		return
	}

	stream, err := s.resolveStream(serverName)
	if err != nil {
		_ = wrappedConn.Close()
		return
	}

	claimCtx, cancel := context.WithTimeout(ctx, s.cfg.ClaimTimeout)
	defer cancel()

	session, err := stream.Claim(claimCtx)
	if err != nil {
		_ = wrappedConn.Close()
		return
	}

	BridgeConns(wrappedConn, session)
}

func (s *Server) bridgeToAPI(ctx context.Context, conn net.Conn) {
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
	BridgeConns(conn, upstream)
}

func (s *Server) lookupRoutableLease(serverName string) (*leaseRecord, error) {
	record, ok := s.registry.Lookup(serverName)
	if !ok || record == nil {
		return nil, errors.New("no route")
	}
	if time.Now().After(record.ExpiresAt) {
		return nil, errors.New("lease expired")
	}
	if !s.registry.policy.IsLeaseRoutable(record.ID) {
		return nil, errors.New("not routable")
	}
	return record, nil
}

func (s *Server) resolveStream(serverName string) (*transport.RelayStream, error) {
	record, err := s.lookupRoutableLease(serverName)
	if err != nil {
		return nil, err
	}
	if record.stream == nil {
		return nil, errors.New("transport mismatch")
	}
	return record.stream, nil
}

func (s *Server) datagramPlaneReady() bool {
	if s == nil || s.cfg.UDPPortCount <= 0 {
		return false
	}
	if s.group == nil {
		return true
	}
	return s.quicTunnel != nil
}

func (s *Server) requireDatagramPlane(udpEnabled bool) error {
	if !udpEnabled {
		return nil
	}
	if s.datagramPlaneReady() {
		return nil
	}
	return errFeatureUnavailable
}

func (s *Server) admitLeaseByID(leaseID, token string, requireDatagram bool) (*leaseRecord, error) {
	record, err := s.registry.FindByID(leaseID)
	if err != nil {
		return nil, err
	}
	if !s.registry.policy.IsLeaseRoutable(record.ID) {
		return nil, errLeaseRejected
	}
	if err := s.authorizeLeaseToken(record, token); err != nil {
		return nil, err
	}
	if record.stream == nil {
		return nil, errTransportMismatch
	}
	if requireDatagram && record.datagram == nil {
		return nil, errTransportMismatch
	}
	return record, nil
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

func (s *Server) closeLease(record *leaseRecord) {
	if record == nil {
		return
	}
	record.Close()
}

func (s *Server) watchContext(ctx context.Context) error {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.Shutdown(shutdownCtx)
}

func (s *Server) desiredWireGuardPeers() []types.DesiredPeer {
	if s.wgRuntime == nil {
		return nil
	}

	snapshot := s.discoveryCache.Snapshot()
	peers := make([]types.DesiredPeer, 0, len(snapshot))
	for _, state := range snapshot {
		if state.State != types.PeerStateVerified && state.State != types.PeerStateAdvertised {
			continue
		}
		desc := state.Descriptor
		if desc.RelayID == s.cfg.PortalURL || !desc.SupportsOverlayPeer {
			continue
		}
		if strings.TrimSpace(desc.WireGuardPublicKey) == "" || strings.TrimSpace(desc.WireGuardEndpoint) == "" || strings.TrimSpace(desc.OverlayIPv4) == "" {
			continue
		}

		allowedIPs := []string{desc.OverlayIPv4 + "/32"}
		allowedIPs = append(allowedIPs, desc.OverlayCIDRs...)
		peers = append(peers, types.DesiredPeer{
			RelayID:            desc.RelayID,
			WireGuardPublicKey: desc.WireGuardPublicKey,
			WireGuardEndpoint:  desc.WireGuardEndpoint,
			AllowedIPs:         allowedIPs,
		})
	}
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].RelayID < peers[j].RelayID
	})
	return peers
}

func (s *Server) syncWireGuardPeers() error {
	if s.wgRuntime == nil {
		return nil
	}
	return s.wgRuntime.ApplyPeers(s.desiredWireGuardPeers())
}

func (s *Server) discoverRelay(ctx context.Context, peer types.RelayDescriptor) (types.DiscoverResponse, error) {
	if peer.SupportsOverlayPeer && s.discoveryCache.HasPinnedIdentity(peer.RelayID) {
		state, ok := s.discoveryCache.Lookup(peer.RelayID)
		if ok && state.State != types.PeerStateExpired && s.wgRuntime != nil {
			if strings.TrimSpace(peer.OverlayIPv4) == "" {
				return types.DiscoverResponse{}, errors.New("relay peer is missing overlay ipv4")
			}
			return s.wgRuntime.Discover(ctx, peer.OverlayIPv4, wireguard.DefaultPeerAPIHTTPPort, types.DiscoverRequest{})
		}
	}

	seedURL := strings.TrimSpace(s.discoveryCache.SeedURL(peer.RelayID))
	if seedURL == "" {
		seedURL = strings.TrimSpace(peer.APIHTTPSAddr)
	}
	if seedURL == "" {
		return types.DiscoverResponse{}, errors.New("relay peer is missing seed url")
	}
	return discovery.Discover(ctx, seedURL, types.DiscoverRequest{}, nil)
}

func (s *Server) runDiscoveryLoop(ctx context.Context) error {
	ticker := time.NewTicker(defaultDiscoveryInterval)
	defer ticker.Stop()

	for {
		peers := s.discoveryCache.KnownDescriptors()
		if len(peers) > 0 {
			for _, peer := range peers {
				resp, err := s.discoverRelay(ctx, peer)
				switch {
				case err == nil:
					selfDescriptor, peerDescriptors, resolveErr := discovery.ResolvePeerResponse(resp, time.Now().UTC())
					if strings.TrimSpace(selfDescriptor.RelayID) == "" {
						s.discoveryCache.RecordFailure(peer.RelayID)
						log.Warn().
							Err(resolveErr).
							Str("peer", peer.APIHTTPSAddr).
							Msg("discovery response missing valid self descriptor")
						continue
					}
					if resolveErr != nil {
						log.Warn().
							Err(resolveErr).
							Str("peer", peer.APIHTTPSAddr).
							Msg("discovery response contained invalid peer descriptors")
					}
					if seedURL := strings.TrimSpace(s.discoveryCache.SeedURL(peer.RelayID)); seedURL != "" {
						if err := s.discoveryCache.PinIdentity(peer.RelayID, seedURL, selfDescriptor); err != nil {
							s.discoveryCache.RecordFailure(peer.RelayID)
							log.Warn().
								Err(err).
								Str("peer", peer.APIHTTPSAddr).
								Msg("discovery peer identity pin failed")
							continue
						}
					}

					peerSetChanged := false
					added, changed, err := s.discoveryCache.RecordVerified(selfDescriptor, true)
					if err != nil {
						s.discoveryCache.RecordFailure(selfDescriptor.RelayID)
						log.Warn().
							Err(err).
							Str("peer", peer.APIHTTPSAddr).
							Msg("record self discovery peer failed")
						continue
					}
					peerSetChanged = peerSetChanged || changed
					addedHints := make([]string, 0, len(peerDescriptors))
					for _, peerDescriptor := range peerDescriptors {
						hintAdded, hintChanged, err := s.discoveryCache.RecordVerified(peerDescriptor, false)
						if err != nil {
							log.Warn().
								Err(err).
								Str("peer", peerDescriptor.RelayID).
								Msg("record hinted discovery peer failed")
							continue
						}
						peerSetChanged = peerSetChanged || hintChanged
						if hintAdded || hintChanged {
							addedHints = append(addedHints, peerDescriptor.APIHTTPSAddr)
						}
					}
					if peerSetChanged {
						if err := s.syncWireGuardPeers(); err != nil {
							log.Warn().
								Err(err).
								Str("peer", peer.APIHTTPSAddr).
								Msg("sync wireguard peers failed")
						}
					}

					if added || changed || len(addedHints) > 0 {
						log.Info().
							Str("peer", peer.APIHTTPSAddr).
							Bool("discoverable", selfDescriptor.SupportsOverlayPeer).
							Int("hint_count", len(addedHints)).
							Int("known_count", len(s.discoveryCache.KnownDescriptors())).
							Int("advertised_count", len(s.discoveryCache.AdvertisedDescriptors())).
							Strs("added_hints", addedHints).
							Msg("discovery peer state updated")
					}
				case ctx.Err() != nil:
					return nil
				default:
					s.discoveryCache.RecordFailure(peer.RelayID)
					if state, ok := s.discoveryCache.Lookup(peer.RelayID); ok &&
						state.State != types.PeerStateExpired &&
						peer.SupportsOverlayPeer &&
						s.discoveryCache.HasPinnedIdentity(peer.RelayID) &&
						state.ConsecutiveFailures >= defaultWGRecoveryFailures {
						if removed := s.discoveryCache.Expire(peer.RelayID); removed {
							if err := s.syncWireGuardPeers(); err != nil {
								log.Warn().
									Err(err).
									Str("peer", peer.APIHTTPSAddr).
									Msg("sync wireguard peers failed")
							}
							log.Warn().
								Int("consecutive_failures", state.ConsecutiveFailures).
								Str("peer", peer.APIHTTPSAddr).
								Msg("wireguard discovery failed repeatedly, forcing seed re-hydration")
						}
					}
					var apiErr *types.APIRequestError
					if errors.As(err, &apiErr) &&
						(apiErr.StatusCode == http.StatusForbidden ||
							apiErr.StatusCode == http.StatusNotFound ||
							apiErr.StatusCode == http.StatusGone) {
						if removed := s.discoveryCache.Expire(peer.RelayID); removed {
							if err := s.syncWireGuardPeers(); err != nil {
								log.Warn().
									Err(err).
									Str("peer", peer.APIHTTPSAddr).
									Msg("sync wireguard peers failed")
							}
							log.Info().
								Str("peer", peer.APIHTTPSAddr).
								Msg("discovery peer removed from advertised set")
						}
					}

					log.Warn().
						Err(err).
						Str("peer", peer.APIHTTPSAddr).
						Msg("discover peer failed")
				}
			}
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
