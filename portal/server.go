package portal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"crypto/tls"

	"github.com/gosuda/keyless_tls/relay/l4"
	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"

	"github.com/gosuda/portal/v2/portal/acme"
	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/utils"
)

const (
	defaultLeaseTTL          = 30 * time.Second
	defaultClaimTimeout      = 10 * time.Second
	defaultIdleKeepalive     = 15 * time.Second
	defaultReadyQueueLimit   = 8
	defaultClientHelloWait   = 2 * time.Second
	defaultControlBodyLimit  = 4 << 20
	defaultSessionWriteLimit = 5 * time.Second

	defaultUDPPortMin = 29000
	defaultUDPPortMax = 29999
)

type ServerConfig struct {
	PortalURL             string
	ACME                  acme.Config
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
	UDPPortMin            int
	UDPPortMax            int
}

type Server struct {
	sniListener  net.Listener
	apiListener  net.Listener
	apiServer    *http.Server
	apiTLSClose  io.Closer
	acmeManager  *acme.Manager
	quicTunnel   *quicTunnelListener
	quicSNIConn  net.PacketConn
	cancel       context.CancelFunc
	group        *errgroup.Group
	registry     *leaseRegistry
	ports        *portAllocator
	cfg          ServerConfig
	rootHost     string
	shutdownOnce sync.Once
}

func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.APIListenAddr == "" {
		cfg.APIListenAddr = ":4017"
	}
	if cfg.SNIListenAddr == "" {
		cfg.SNIListenAddr = ":443"
	}
	cfg.LeaseTTL = utils.DurationOrDefault(cfg.LeaseTTL, defaultLeaseTTL)
	cfg.ClaimTimeout = utils.DurationOrDefault(cfg.ClaimTimeout, defaultClaimTimeout)
	cfg.IdleKeepaliveInterval = utils.DurationOrDefault(cfg.IdleKeepaliveInterval, defaultIdleKeepalive)
	cfg.ReadyQueueLimit = utils.IntOrDefault(cfg.ReadyQueueLimit, defaultReadyQueueLimit)
	cfg.ClientHelloTimeout = utils.DurationOrDefault(cfg.ClientHelloTimeout, defaultClientHelloWait)
	trustedProxyCIDRs, err := policy.ParseTrustedProxyCIDRs(cfg.TrustedProxyCIDRs)
	if err != nil {
		return nil, fmt.Errorf("parse trusted proxy cidrs: %w", err)
	}
	policy.SetTrustedProxyCIDRs(trustedProxyCIDRs)
	rootHost := utils.PortalRootHost(cfg.PortalURL)
	if rootHost == "" {
		return nil, errors.New("root host is required")
	}
	cfg.UDPPortMin = utils.IntOrDefault(cfg.UDPPortMin, defaultUDPPortMin)
	cfg.UDPPortMax = utils.IntOrDefault(cfg.UDPPortMax, defaultUDPPortMax)
	if cfg.QUICListenAddr == "" {
		cfg.QUICListenAddr = cfg.APIListenAddr
	}

	registry := newLeaseRegistry(policy.NewRuntime())
	ports := newPortAllocator(cfg.UDPPortMin, cfg.UDPPortMax, 5*time.Minute)

	s := &Server{
		cfg:      cfg,
		rootHost: rootHost,
		registry: registry,
		ports:    ports,
	}

	// Tear down all lease resources when leases expire via TTL janitor.
	registry.onExpired = func(record *leaseRecord) {
		s.closeLease(record)
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

	group.Go(s.runAPIServer)
	group.Go(func() error { return s.runSNIListener(groupCtx) })
	group.Go(func() error { return s.registry.RunJanitor(groupCtx, 5*time.Second) })
	group.Go(func() error { return s.watchContext(groupCtx) })
	s.acmeManager.Start(serverCtx)

	if err := s.startQUICTunnelListener(serverCtx, apiTLS); err != nil {
		log.Warn().Err(err).Msg("quic tunnel listener disabled")
	}
	if err := s.startQUICSNIRouter(); err != nil {
		log.Warn().Err(err).Msg("quic sni router disabled")
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
			_ = s.quicTunnel.close()
		}
		if s.quicSNIConn != nil {
			_ = s.quicSNIConn.Close()
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
	return s.registry.PolicyRuntime()
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

func (s *Server) QUICAddr() string {
	if s.quicTunnel == nil || s.quicTunnel.listener == nil {
		return ""
	}
	return s.quicTunnel.listener.Addr().String()
}

func (s *Server) GetLease(leaseID string) (LeaseSnapshot, bool) {
	record, ok := s.registry.Get(leaseID)
	if !ok {
		return LeaseSnapshot{}, false
	}
	return s.registry.Snapshot(record), true
}

func (s *Server) ListLeases() []LeaseSnapshot {
	records := s.registry.List()
	out := make([]LeaseSnapshot, 0, len(records))
	for _, record := range records {
		out = append(out, s.registry.Snapshot(record))
	}
	return out
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
		_ = wrappedConn.Close()
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

	record, ok := s.registry.Lookup(serverName)
	if !ok || time.Now().After(record.ExpiresAt) {
		_ = wrappedConn.Close()
		return
	}
	if !s.registry.IsRoutable(record) {
		_ = wrappedConn.Close()
		return
	}

	claimCtx, cancel := context.WithTimeout(ctx, s.cfg.ClaimTimeout)
	defer cancel()

	session, err := record.Broker.Claim(claimCtx)
	if err != nil {
		_ = wrappedConn.Close()
		return
	}

	BridgeConns(wrappedConn, session.Conn())
	_ = session.Close()
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

func (s *Server) watchContext(ctx context.Context) error {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.Shutdown(shutdownCtx)
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

func (s *Server) startQUICTunnelListener(ctx context.Context, apiTLS keyless.TLSMaterialConfig) error {
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
		EnableDatagrams:   true,
		KeepAlivePeriod:   15 * time.Second,
		MaxIdleTimeout:    60 * time.Second,
		MaxIncomingStreams: 16,
	}

	listener, err := quic.ListenAddr(s.cfg.QUICListenAddr, tlsConf, quicConf)
	if err != nil {
		return fmt.Errorf("listen quic: %w", err)
	}

	tunnel := newQUICTunnelListener(listener, s)
	s.quicTunnel = tunnel
	s.group.Go(tunnel.run)

	log.Info().
		Str("component", "relay-server").
		Str("quic_addr", listener.Addr().String()).
		Msg("quic tunnel listener started")
	return nil
}

func (s *Server) startQUICSNIRouter() error {
	conn, err := net.ListenPacket("udp", s.cfg.SNIListenAddr)
	if err != nil {
		return fmt.Errorf("listen quic sni udp: %w", err)
	}

	router := newQUICSNIRouter(conn, s)
	s.quicSNIConn = conn
	s.group.Go(router.run)

	log.Info().
		Str("component", "relay-server").
		Str("quic_sni_addr", conn.LocalAddr().String()).
		Msg("quic sni router started")
	return nil
}

func (s *Server) startUDPRelay(ctx context.Context, leaseID string, relay *udpRelay) {
	if relay == nil {
		return
	}
	if err := relay.Start(ctx); err != nil {
		log.Error().
			Err(err).
			Str("component", "relay-server").
			Str("lease_id", leaseID).
			Msg("failed to start udp relay")
	}
}

// closeLease tears down all resources associated with a single lease record.
func (s *Server) closeLease(record *leaseRecord) {
	record.Broker.Close()
	if record.QUICBroker != nil {
		record.QUICBroker.Stop()
	}
	if record.UDPRelay != nil {
		record.UDPRelay.Stop()
	}
	if record.UDPPort > 0 && s.ports != nil {
		s.ports.Release(record.UDPPort)
	}
}

func (s *Server) quicPublicAddr() string {
	_, port, err := net.SplitHostPort(s.cfg.QUICListenAddr)
	if err != nil {
		port = "4017"
	}
	return net.JoinHostPort(s.rootHost, port)
}
