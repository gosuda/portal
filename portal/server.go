package portal

import (
	"context"
	"encoding/json"
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
	portaldatagram "github.com/gosuda/portal/v2/portal/datagram"
	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/types"
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
	defaultQUICSNIRouteIdle  = 30 * time.Second
	defaultQUICSNICleanup    = 5 * time.Second

	defaultUDPPortMin = 29000
	defaultUDPPortMax = 29999
)

type quicSNIRoute struct {
	flowMux  *portaldatagram.FlowMux
	lastSeen time.Time
}

type ServerConfig struct {
	PortalURL             string
	ACME                  acme.Config
	APIListenAddr         string
	SNIListenAddr         string
	QUICListenAddr        string
	TrustedProxyCIDRs     []*net.IPNet
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
	sniListener   net.Listener
	apiListener   net.Listener
	apiServer     *http.Server
	apiTLSClose   io.Closer
	acmeManager   *acme.Manager
	quicTunnel    *quic.Listener
	quicSNI       net.PacketConn
	cancel        context.CancelFunc
	group         *errgroup.Group
	registry      *leaseRegistry
	ports         *portaldatagram.PortAllocator
	cfg           ServerConfig
	rootHost      string
	shutdownOnce  sync.Once
	quicSNIRoutes map[string]quicSNIRoute
	quicSNIMu     sync.RWMutex
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
	ports := portaldatagram.NewPortAllocator(cfg.UDPPortMin, cfg.UDPPortMax, 5*time.Minute)

	s := &Server{
		cfg:           cfg,
		rootHost:      rootHost,
		registry:      registry,
		ports:         ports,
		quicSNIRoutes: make(map[string]quicSNIRoute),
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

	if err := s.startQUICTunnelListener(apiTLS); err != nil {
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
			_ = s.quicTunnel.Close()
		}
		if s.quicSNI != nil {
			_ = s.quicSNI.Close()
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

func (s *Server) QUICAddr() string {
	if s.quicTunnel == nil {
		return ""
	}
	return s.quicTunnel.Addr().String()
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

	broker, err := s.resolveStreamBroker(serverName)
	if err != nil {
		_ = wrappedConn.Close()
		return
	}

	claimCtx, cancel := context.WithTimeout(ctx, s.cfg.ClaimTimeout)
	defer cancel()

	session, err := broker.Claim(claimCtx)
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

func (s *Server) resolveStreamBroker(serverName string) (*streamBroker, error) {
	record, err := s.lookupRoutableLease(serverName)
	if err != nil {
		return nil, err
	}
	if !record.SupportsStream() {
		return nil, errors.New("transport mismatch")
	}
	streamBroker := record.StreamBroker()
	if streamBroker == nil {
		return nil, errors.New("stream broker unavailable")
	}
	return streamBroker, nil
}

func (s *Server) resolveDatagramFlowMux(serverName string) (*portaldatagram.FlowMux, error) {
	if serverName == s.rootHost {
		return nil, errors.New("root host does not accept datagram routes")
	}

	record, err := s.lookupRoutableLease(serverName)
	if err != nil {
		return nil, err
	}
	if !record.SupportsDatagram() {
		return nil, errors.New("transport mismatch")
	}
	flowMux := record.DatagramFlowMux()
	if flowMux == nil {
		return nil, errors.New("flow mux unavailable")
	}
	return flowMux, nil
}

type quicControlMessage struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
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
		Str("component", "relay-server").
		Str("quic_addr", listener.Addr().String()).
		Msg("quic tunnel listener started")
	return nil
}

func (s *Server) startQUICSNIRouter() error {
	var listenConfig net.ListenConfig
	conn, err := listenConfig.ListenPacket(context.Background(), "udp", s.cfg.SNIListenAddr)
	if err != nil {
		return fmt.Errorf("listen quic sni udp: %w", err)
	}

	s.quicSNI = conn
	s.group.Go(func() error { return s.runQUICSNIRouter(conn) })

	log.Info().
		Str("component", "relay-server").
		Str("quic_sni_addr", conn.LocalAddr().String()).
		Msg("quic sni router started")
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

func (s *Server) handleQUICTunnelConn(conn *quic.Conn) {
	stream, err := conn.AcceptStream(context.Background())
	if err != nil {
		_ = conn.CloseWithError(1, "stream accept failed")
		return
	}

	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	var msg quicControlMessage
	buf := make([]byte, 4096)
	n, err := stream.Read(buf)
	if err != nil {
		_ = conn.CloseWithError(1, "control read failed")
		return
	}
	if err := json.Unmarshal(buf[:n], &msg); err != nil {
		_, _ = stream.Write([]byte(`{"ok":false,"error":"invalid_control_message"}`))
		_ = conn.CloseWithError(1, "invalid control message")
		return
	}
	_ = stream.SetReadDeadline(time.Time{})

	lease, err := s.findLeaseByID(msg.LeaseID)
	switch {
	case err != nil:
		_, _ = stream.Write([]byte(`{"ok":false,"error":"lease_not_found"}`))
		_ = conn.CloseWithError(1, "lease not found")
		return
	case s.authorizeLeaseToken(lease, msg.ReverseToken) != nil:
		_, _ = stream.Write([]byte(`{"ok":false,"error":"unauthorized"}`))
		_ = conn.CloseWithError(1, "unauthorized")
		return
	}

	flowMux := lease.DatagramFlowMux()
	if flowMux == nil {
		_, _ = stream.Write([]byte(`{"ok":false,"error":"transport_mismatch"}`))
		_ = conn.CloseWithError(1, "transport mismatch")
		return
	}
	if err := flowMux.Register(conn); err != nil {
		_, _ = stream.Write([]byte(`{"ok":false,"error":"broker_closed"}`))
		_ = conn.CloseWithError(1, "broker closed")
		return
	}

	_, _ = stream.Write([]byte(`{"ok":true}`))
	s.registry.Touch(lease.ID, conn.RemoteAddr().String(), time.Now())
	log.Info().
		Str("component", "quic-tunnel-listener").
		Str("lease_id", lease.ID).
		Str("lease_name", lease.Name).
		Str("remote_addr", conn.RemoteAddr().String()).
		Msg("quic tunnel connected")
}

func (s *Server) runQUICSNIRouter(conn net.PacketConn) error {
	buf := make([]byte, 65535)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(defaultQUICSNICleanup))
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				s.cleanupQUICSNIRoutes(time.Now())
				continue
			}
			return err
		}

		packet := make([]byte, n)
		copy(packet, buf[:n])
		s.handleQUICSNIPacket(packet, addr, time.Now())
	}
}

func (s *Server) handleQUICSNIPacket(packet []byte, srcAddr net.Addr, now time.Time) {
	cacheKey := srcAddr.String()
	flowMux, ok := s.lookupQUICSNIRoute(cacheKey, now)
	if !ok {
		serverName, err := portaldatagram.ParseQUICInitialSNI(packet)
		if err != nil || serverName == "" {
			return
		}

		flowMux, err = s.resolveDatagramFlowMux(utils.NormalizeHostname(serverName))
		if err != nil || flowMux == nil {
			return
		}
		s.storeQUICSNIRoute(cacheKey, flowMux, now)
	}

	udpAddr, ok := srcAddr.(*net.UDPAddr)
	if !ok {
		return
	}
	if s.quicSNI == nil {
		return
	}

	flowID := flowMux.TouchFlow("quic:"+cacheKey, func(payload []byte) error {
		_, err := s.quicSNI.WriteTo(payload, udpAddr)
		return err
	})
	if err := flowMux.SendDatagram(flowID, packet); err != nil {
		s.deleteQUICSNIRoute(cacheKey)
	}
}

func (s *Server) lookupQUICSNIRoute(key string, now time.Time) (*portaldatagram.FlowMux, bool) {
	s.quicSNIMu.Lock()
	defer s.quicSNIMu.Unlock()

	route, ok := s.quicSNIRoutes[key]
	if !ok || route.flowMux == nil {
		delete(s.quicSNIRoutes, key)
		return nil, false
	}
	if now.Sub(route.lastSeen) > defaultQUICSNIRouteIdle || !route.flowMux.HasConnection() {
		delete(s.quicSNIRoutes, key)
		return nil, false
	}

	route.lastSeen = now
	s.quicSNIRoutes[key] = route
	return route.flowMux, true
}

func (s *Server) storeQUICSNIRoute(key string, flowMux *portaldatagram.FlowMux, now time.Time) {
	s.quicSNIMu.Lock()
	s.quicSNIRoutes[key] = quicSNIRoute{
		flowMux:  flowMux,
		lastSeen: now,
	}
	s.quicSNIMu.Unlock()
}

func (s *Server) deleteQUICSNIRoute(key string) {
	s.quicSNIMu.Lock()
	delete(s.quicSNIRoutes, key)
	s.quicSNIMu.Unlock()
}

func (s *Server) cleanupQUICSNIRoutes(now time.Time) {
	s.quicSNIMu.Lock()
	defer s.quicSNIMu.Unlock()

	for key, route := range s.quicSNIRoutes {
		if route.flowMux == nil || now.Sub(route.lastSeen) > defaultQUICSNIRouteIdle || !route.flowMux.HasConnection() {
			delete(s.quicSNIRoutes, key)
		}
	}
}

func (s *Server) clearQUICSNIRoutesForFlowMux(flowMux *portaldatagram.FlowMux) {
	if flowMux == nil {
		return
	}

	s.quicSNIMu.Lock()
	defer s.quicSNIMu.Unlock()

	for key, route := range s.quicSNIRoutes {
		if route.flowMux == flowMux {
			delete(s.quicSNIRoutes, key)
		}
	}
}

// closeLease tears down all resources associated with a single lease record.
func (s *Server) closeLease(record *leaseRecord) {
	if record == nil || record.Runtime == nil {
		return
	}
	s.clearQUICSNIRoutesForFlowMux(record.DatagramFlowMux())
	record.Runtime.Close(s.ports)
}

func (s *Server) quicPublicAddr() string {
	_, port, err := net.SplitHostPort(s.cfg.QUICListenAddr)
	if err != nil {
		port = "4017"
	}
	return net.JoinHostPort(s.rootHost, port)
}
