package portal

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"

	"github.com/gosuda/keyless_tls/relay/l4"

	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/types"
)

type ServerConfig struct {
	APIHandlerWrapper     func(http.Handler) http.Handler
	KeylessSignerHandler  http.Handler
	Policy                *policy.Runtime
	PortalURL             string
	APIListenAddr         string
	SNIListenAddr         string
	QUICListenAddr        string
	RootHost              string
	RootFallbackAddr      string
	APITLS                keyless.TLSMaterialConfig
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
	sniListener        net.Listener
	apiTLSClose        io.Closer
	apiListener        net.Listener
	apiServer          *http.Server
	quicTunnel         *quicTunnelListener
	quicSNIConn        net.PacketConn
	ctxDone            <-chan struct{}
	baseContext         func() context.Context
	cancel             context.CancelFunc
	group              *errgroup.Group
	routes             *routeTable
	leases             map[string]*leaseRecord
	ports              *portAllocator
	udpRelays          map[string]*udpRelay
	cfg                ServerConfig
	mu                 sync.RWMutex
	shutdownOnce       sync.Once
}

type leaseRecord struct {
	ExpiresAt    time.Time
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	Broker       *leaseBroker
	QUICBroker   *quicBroker
	ID           string
	Name         string
	ReverseToken string
	ClientIP     string
	Transport    string
	Hostnames    []string
	Metadata     types.LeaseMetadata
	UDPPort      int
}

type LeaseSnapshot struct {
	ExpiresAt   time.Time
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	ID          string
	Name        string
	ClientIP    string
	Transport   string
	Hostnames   []string
	Metadata    types.LeaseMetadata
	Ready       int
	UDPPort     int
	IsApproved  bool
	IsBanned    bool
	IsDenied    bool
	IsIPBanned  bool
}

func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.APIListenAddr == "" {
		cfg.APIListenAddr = ":4017"
	}
	if cfg.SNIListenAddr == "" {
		cfg.SNIListenAddr = ":443"
	}
	cfg.LeaseTTL = durationOrDefault(cfg.LeaseTTL, defaultLeaseTTL)
	cfg.ClaimTimeout = durationOrDefault(cfg.ClaimTimeout, defaultClaimTimeout)
	cfg.IdleKeepaliveInterval = durationOrDefault(cfg.IdleKeepaliveInterval, defaultIdleKeepalive)
	cfg.ReadyQueueLimit = intOrDefault(cfg.ReadyQueueLimit, defaultReadyQueueLimit)
	cfg.ClientHelloTimeout = durationOrDefault(cfg.ClientHelloTimeout, defaultClientHelloWait)
	if cfg.RootHost == "" {
		cfg.RootHost = PortalRootHost(cfg.PortalURL)
	}
	if cfg.Policy == nil {
		cfg.Policy = policy.NewRuntime()
	}
	if cfg.RootHost == "" {
		return nil, errors.New("root host is required")
	}
	if len(cfg.APITLS.CertPEM) == 0 {
		return nil, errors.New("api tls certificate is required")
	}
	if len(cfg.APITLS.KeyPEM) == 0 && cfg.APITLS.Keyless == nil {
		return nil, errors.New("api tls key or keyless signer is required")
	}
	cfg.UDPPortMin = intOrDefault(cfg.UDPPortMin, defaultUDPPortMin)
	cfg.UDPPortMax = intOrDefault(cfg.UDPPortMax, defaultUDPPortMax)
	if cfg.QUICListenAddr == "" {
		cfg.QUICListenAddr = cfg.APIListenAddr
	}

	return &Server{
		cfg:       cfg,
		routes:    newRouteTable(),
		leases:    make(map[string]*leaseRecord),
		ports:     newPortAllocator(cfg.UDPPortMin, cfg.UDPPortMax),
		udpRelays: make(map[string]*udpRelay),
	}, nil
}

func (s *Server) Start(ctx context.Context) error {
	if s.group != nil {
		return errors.New("server already started")
	}

	serverCtx, cancel := context.WithCancel(ctx)
	var listenConfig net.ListenConfig

	apiListener, err := listenConfig.Listen(serverCtx, "tcp", s.cfg.APIListenAddr)
	if err != nil {
		cancel()
		return fmt.Errorf("listen api: %w", err)
	}
	sniListener, err := listenConfig.Listen(serverCtx, "tcp", s.cfg.SNIListenAddr)
	if err != nil {
		_ = apiListener.Close()
		cancel()
		return fmt.Errorf("listen sni: %w", err)
	}

	group, groupCtx := errgroup.WithContext(serverCtx)

	apiServer := &http.Server{
		Handler:           s.wrapAPIHandler(s.apiHandler()),
		ReadHeaderTimeout: 10 * time.Second,
		TLSNextProto:      make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}
	apiCloser, err := keyless.AttachToHTTPServer(apiServer, s.cfg.APITLS)
	if err != nil {
		_ = apiListener.Close()
		_ = sniListener.Close()
		cancel()
		return fmt.Errorf("configure api tls: %w", err)
	}

	s.apiListener = tls.NewListener(apiListener, apiServer.TLSConfig)
	s.sniListener = sniListener
	s.apiServer = apiServer
	s.apiTLSClose = apiCloser
	s.baseContext = func() context.Context { return groupCtx }
	s.ctxDone = groupCtx.Done()
	s.cancel = cancel
	s.group = group

	group.Go(s.runAPIServer)
	group.Go(s.runSNIListener)
	group.Go(s.runLeaseJanitor)
	group.Go(s.watchContext)

	// QUIC tunnel listener for UDP transport reverse sessions.
	if err := s.startQUICTunnelListener(serverCtx); err != nil {
		log.Warn().Err(err).Msg("quic tunnel listener disabled")
	}

	// QUIC SNI router on :443/udp for public QUIC client routing.
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

		s.mu.Lock()
		for _, lease := range s.leases {
			lease.Broker.Stop()
			if lease.QUICBroker != nil {
				lease.QUICBroker.Stop()
			}
		}
		for _, relay := range s.udpRelays {
			relay.Stop()
		}
		s.mu.Unlock()

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
	})
	return shutdownErr
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
	s.mu.RLock()
	record, ok := s.leases[strings.TrimSpace(leaseID)]
	s.mu.RUnlock()
	if !ok {
		return LeaseSnapshot{}, false
	}
	return s.snapshotForLease(record), true
}

func (s *Server) ListLeases() []LeaseSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]LeaseSnapshot, 0, len(s.leases))
	for _, record := range s.leases {
		out = append(out, s.snapshotForLease(record))
	}
	return out
}

func (s *Server) apiHandler() http.Handler {
	mux := http.NewServeMux()
	if s.cfg.KeylessSignerHandler != nil {
		mux.Handle(types.PathV1Sign, s.cfg.KeylessSignerHandler)
	}
	mux.HandleFunc(types.PathHealthz, s.handleHealthz)
	mux.HandleFunc(types.PathSDKDomain, s.handleDomain)
	mux.HandleFunc(types.PathSDKRegister, s.handleRegister)
	mux.HandleFunc(types.PathSDKRenew, s.handleRenew)
	mux.HandleFunc(types.PathSDKUnregister, s.handleUnregister)
	mux.HandleFunc(types.PathSDKConnect, s.handleConnect)
	mux.HandleFunc("/", s.handleRoot)
	return mux
}

func (s *Server) handleRoot(w http.ResponseWriter, _ *http.Request) {
	writeAPIData(w, http.StatusOK, map[string]any{
		"service": "portal-relay",
		"root":    s.cfg.RootHost,
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeAPIData(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleDomain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}
	name := r.URL.Query().Get("name")
	writeAPIData(w, http.StatusOK, types.DomainResponse{
		RootHost:          s.cfg.RootHost,
		SuggestedHostname: suggestHostname(name, s.cfg.RootHost),
	})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}
	clientIP := s.clientIPFromRequest(r)
	if s.isClientIPBanned(clientIP) {
		writeAPIError(w, http.StatusForbidden, types.APIErrorCodeIPBanned, "request denied because source IP is banned")
		return
	}
	var req types.RegisterRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidJSON, err.Error())
		return
	}
	resp, err := s.registerLease(req, clientIP)
	if err != nil {
		status, code := http.StatusBadRequest, types.APIErrorCodeInvalidRequest
		if errors.Is(err, errHostnameConflict) {
			status, code = http.StatusConflict, types.APIErrorCodeHostnameConflict
		}
		if errors.Is(err, errIPBanned) {
			status, code = http.StatusForbidden, types.APIErrorCodeIPBanned
		}
		if errors.Is(err, errPortExhausted) {
			status, code = http.StatusServiceUnavailable, types.APIErrorCodeUDPPortExhausted
		}
		writeAPIError(w, status, code, err.Error())
		return
	}

	// Start UDP relay outside the lock if the lease needs one.
	if resp.UDPAddr != "" {
		s.startUDPRelay(resp.LeaseID)
	}

	writeAPIData(w, http.StatusCreated, resp)
}

func (s *Server) handleRenew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}
	clientIP := s.clientIPFromRequest(r)
	if s.isClientIPBanned(clientIP) {
		writeAPIError(w, http.StatusForbidden, types.APIErrorCodeIPBanned, "request denied because source IP is banned")
		return
	}
	var req types.RenewRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidJSON, err.Error())
		return
	}
	resp, err := s.renewLease(req, clientIP)
	if err != nil {
		status, code := http.StatusBadRequest, types.APIErrorCodeInvalidRequest
		if errors.Is(err, errLeaseNotFound) {
			status, code = http.StatusNotFound, types.APIErrorCodeLeaseNotFound
		}
		if errors.Is(err, errUnauthorized) {
			status, code = http.StatusForbidden, types.APIErrorCodeUnauthorized
		}
		if errors.Is(err, errIPBanned) {
			status, code = http.StatusForbidden, types.APIErrorCodeIPBanned
		}
		writeAPIError(w, status, code, err.Error())
		return
	}
	writeAPIData(w, http.StatusOK, resp)
}

func (s *Server) handleUnregister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}
	var req types.UnregisterRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidJSON, err.Error())
		return
	}
	if err := s.unregisterLease(req); err != nil {
		status, code := http.StatusBadRequest, types.APIErrorCodeInvalidRequest
		if errors.Is(err, errLeaseNotFound) {
			status, code = http.StatusNotFound, types.APIErrorCodeLeaseNotFound
		}
		if errors.Is(err, errUnauthorized) {
			status, code = http.StatusForbidden, types.APIErrorCodeUnauthorized
		}
		writeAPIError(w, status, code, err.Error())
		return
	}
	writeAPIOK(w, http.StatusOK)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}
	if r.ProtoMajor != 1 {
		writeAPIError(w, http.StatusHTTPVersionNotSupported, types.APIErrorCodeHTTP11Only, "reverse connect requires HTTP/1.1")
		return
	}

	leaseID := strings.TrimSpace(r.URL.Query().Get("lease_id"))
	token := strings.TrimSpace(r.Header.Get(types.HeaderReverseToken))
	clientIP := s.clientIPFromRequest(r)
	if s.isClientIPBanned(clientIP) {
		writeAPIError(w, http.StatusForbidden, types.APIErrorCodeIPBanned, "request denied because source IP is banned")
		return
	}

	lease, err := s.findLeaseByID(leaseID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, types.APIErrorCodeLeaseNotFound, err.Error())
		return
	}
	if !s.isLeaseRoutable(lease) {
		writeAPIError(w, http.StatusForbidden, types.APIErrorCodeLeaseRejected, "lease is not approved for routing")
		return
	}
	if authErr := s.authorizeLeaseToken(lease, token); authErr != nil {
		writeAPIError(w, http.StatusForbidden, types.APIErrorCodeUnauthorized, authErr.Error())
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, types.APIErrorCodeHijackUnsupported, "hijacking is not supported")
		return
	}

	conn, rw, err := hijacker.Hijack()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, types.APIErrorCodeHijackFailed, err.Error())
		return
	}

	if _, err := rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: keep-alive\r\n\r\n"); err != nil {
		_ = conn.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return
	}

	session := newReverseSession(conn, s.cfg.IdleKeepaliveInterval)
	if err := lease.Broker.Offer(session); err != nil {
		log.Warn().
			Err(err).
			Str("component", "relay-server").
			Str("lease_id", lease.ID).
			Str("lease_name", lease.Name).
			Str("remote_addr", session.RemoteAddr()).
			Msg("sdk reverse rejected")
		_ = session.Close()
		return
	}
	s.touchLease(lease.ID, clientIP)
	log.Info().
		Str("component", "relay-server").
		Str("lease_id", lease.ID).
		Str("lease_name", lease.Name).
		Str("remote_addr", session.RemoteAddr()).
		Int("ready", lease.Broker.ReadyCount()).
		Msg("sdk reverse connected")
}

func (s *Server) registerLease(req types.RegisterRequest, clientIP string) (types.RegisterResponse, error) {
	if strings.TrimSpace(req.Name) == "" {
		return types.RegisterResponse{}, errors.New("name is required")
	}
	if strings.TrimSpace(req.ReverseToken) == "" {
		return types.RegisterResponse{}, errors.New("reverse token is required")
	}
	if !req.TLS {
		return types.RegisterResponse{}, errors.New("tls must be true")
	}
	if s.isClientIPBanned(clientIP) {
		return types.RegisterResponse{}, errIPBanned
	}

	hostnames := normalizeHostnames(req.Hostnames)
	if len(hostnames) == 0 {
		hostnames = []string{suggestHostname(req.Name, s.cfg.RootHost)}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, host := range hostnames {
		if owner := s.findLeaseByHostnameLocked(host); owner != nil {
			return types.RegisterResponse{}, fmt.Errorf("%w: %s", errHostnameConflict, host)
		}
	}

	ttl := s.cfg.LeaseTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}

	transport := strings.TrimSpace(strings.ToLower(req.Transport))
	if transport == "" {
		transport = types.TransportTCP
	}
	switch transport {
	case types.TransportTCP, types.TransportUDP, types.TransportBoth:
	default:
		return types.RegisterResponse{}, fmt.Errorf("unsupported transport: %s", transport)
	}

	needsUDP := transport == types.TransportUDP || transport == types.TransportBoth
	var udpPort int
	if needsUDP {
		var portErr error
		udpPort, portErr = s.ports.Allocate()
		if portErr != nil {
			return types.RegisterResponse{}, fmt.Errorf("allocate udp port: %w", portErr)
		}
	}

	leaseID := randomID("lease_")
	now := time.Now()
	expiresAt := now.Add(ttl)
	record := &leaseRecord{
		ID:           leaseID,
		Name:         strings.TrimSpace(req.Name),
		Hostnames:    hostnames,
		Metadata:     req.Metadata,
		ReverseToken: req.ReverseToken,
		ExpiresAt:    expiresAt,
		FirstSeenAt:  now,
		LastSeenAt:   now,
		ClientIP:     clientIP,
		Transport:    transport,
		UDPPort:      udpPort,
		Broker:       newLeaseBroker(leaseID, s.cfg.IdleKeepaliveInterval, s.cfg.ReadyQueueLimit),
	}

	if needsUDP {
		record.QUICBroker = newQUICBroker(leaseID)
	}

	s.leases[leaseID] = record
	for _, host := range hostnames {
		s.routes.Set(host, leaseID)
	}
	if strings.TrimSpace(clientIP) != "" {
		s.cfg.Policy.IPFilter().RegisterLeaseIP(leaseID, clientIP)
	}

	// Start UDP relay for leases that need it (done outside lock in a goroutine-safe way).
	if needsUDP {
		relay := newUDPRelay(leaseID, udpPort, record.QUICBroker)
		s.udpRelays[leaseID] = relay
	}

	resp := types.RegisterResponse{
		LeaseID:    leaseID,
		Hostnames:  append([]string(nil), hostnames...),
		Metadata:   record.Metadata,
		ExpiresAt:  expiresAt,
		ConnectURL: s.connectURL(),
		Transport:  transport,
	}
	if needsUDP {
		resp.UDPAddr = fmt.Sprintf("%s:%d", s.cfg.RootHost, udpPort)
		resp.QUICAddr = s.quicPublicAddr()
	}
	return resp, nil
}

func (s *Server) renewLease(req types.RenewRequest, clientIP string) (types.RenewResponse, error) {
	if s.isClientIPBanned(clientIP) {
		return types.RenewResponse{}, errIPBanned
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.leases[strings.TrimSpace(req.LeaseID)]
	if !ok {
		return types.RenewResponse{}, errLeaseNotFound
	}
	if !tokenMatches(record.ReverseToken, req.ReverseToken) {
		return types.RenewResponse{}, errUnauthorized
	}

	ttl := s.cfg.LeaseTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	record.ExpiresAt = time.Now().Add(ttl)
	record.LastSeenAt = time.Now()
	if strings.TrimSpace(clientIP) != "" {
		record.ClientIP = clientIP
		s.cfg.Policy.IPFilter().RegisterLeaseIP(record.ID, clientIP)
	}
	record.Broker.Reset()
	return types.RenewResponse{LeaseID: record.ID, ExpiresAt: record.ExpiresAt}, nil
}

func (s *Server) unregisterLease(req types.UnregisterRequest) error {
	s.mu.Lock()
	record, ok := s.leases[strings.TrimSpace(req.LeaseID)]
	if !ok {
		s.mu.Unlock()
		return errLeaseNotFound
	}
	if !tokenMatches(record.ReverseToken, req.ReverseToken) {
		s.mu.Unlock()
		return errUnauthorized
	}
	relay := s.udpRelays[record.ID]
	delete(s.udpRelays, record.ID)
	delete(s.leases, record.ID)
	s.mu.Unlock()

	s.routes.DeleteLease(record.Hostnames)
	s.cfg.Policy.ForgetLease(record.ID)
	record.Broker.Drop()
	if record.QUICBroker != nil {
		record.QUICBroker.Stop()
	}
	if relay != nil {
		relay.Stop()
	}
	if record.UDPPort > 0 {
		s.ports.Release(record.UDPPort)
	}
	return nil
}

func (s *Server) findLeaseByID(leaseID string) (*leaseRecord, error) {
	s.mu.RLock()
	record, ok := s.leases[strings.TrimSpace(leaseID)]
	s.mu.RUnlock()
	if !ok {
		return nil, errLeaseNotFound
	}
	if time.Now().After(record.ExpiresAt) {
		return nil, errLeaseNotFound
	}
	return record, nil
}

func (s *Server) authorizeLeaseToken(record *leaseRecord, token string) error {
	if record == nil {
		return errLeaseNotFound
	}
	if !tokenMatches(record.ReverseToken, token) {
		return errUnauthorized
	}
	return nil
}

func (s *Server) findLeaseByHostnameLocked(host string) *leaseRecord {
	host = normalizeHostname(host)
	now := time.Now()
	for _, lease := range s.leases {
		if now.After(lease.ExpiresAt) {
			continue
		}
		for _, candidate := range lease.Hostnames {
			if normalizeHostname(candidate) == host {
				return lease
			}
		}
	}
	return nil
}

func (s *Server) runAPIServer() error {
	err := s.apiServer.Serve(s.apiListener)
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (s *Server) runSNIListener() error {
	for {
		conn, err := s.sniListener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || s.isClosed() {
				return nil
			}
			return err
		}
		go s.handleSNIConn(conn)
	}
}

func (s *Server) handleSNIConn(conn net.Conn) {
	clientHello, wrappedConn, err := l4.InspectClientHello(conn, s.cfg.ClientHelloTimeout)
	if err != nil {
		_ = wrappedConn.Close()
		return
	}

	serverName := normalizeHostname(clientHello.ServerName)
	if serverName == "" {
		_ = wrappedConn.Close()
		return
	}

	if serverName == s.cfg.RootHost && s.cfg.RootFallbackAddr != "" {
		s.bridgeToFallback(wrappedConn)
		return
	}

	leaseID, ok := s.routes.Lookup(serverName)
	if !ok {
		_ = wrappedConn.Close()
		return
	}

	s.mu.RLock()
	record := s.leases[leaseID]
	s.mu.RUnlock()
	if record == nil || time.Now().After(record.ExpiresAt) {
		_ = wrappedConn.Close()
		return
	}
	if !s.isLeaseRoutable(record) {
		_ = wrappedConn.Close()
		return
	}

	claimCtx, cancel := context.WithTimeout(s.context(), s.cfg.ClaimTimeout)
	defer cancel()

	session, err := record.Broker.Claim(claimCtx)
	if err != nil {
		_ = wrappedConn.Close()
		return
	}

	bridgeConns(wrappedConn, session.Conn())
	_ = session.Close()
}

func (s *Server) bridgeToFallback(conn net.Conn) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	upstream, err := dialer.DialContext(s.context(), "tcp", HostPortOrLoopback(s.cfg.RootFallbackAddr))
	if err != nil {
		_ = conn.Close()
		return
	}
	bridgeConns(conn, upstream)
}

func (s *Server) runLeaseJanitor() error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctxDone:
			return nil
		case <-ticker.C:
			s.cleanupExpiredLeases()
		}
	}
}

func (s *Server) cleanupExpiredLeases() {
	now := time.Now()

	s.mu.Lock()
	expired := make([]*leaseRecord, 0)
	for leaseID, lease := range s.leases {
		if now.After(lease.ExpiresAt) {
			expired = append(expired, lease)
			delete(s.leases, leaseID)
		}
	}
	s.mu.Unlock()

	for _, lease := range expired {
		s.routes.DeleteLease(lease.Hostnames)
		s.cfg.Policy.ForgetLease(lease.ID)
		lease.Broker.Drop()
		if lease.QUICBroker != nil {
			lease.QUICBroker.Stop()
		}
		if lease.UDPPort > 0 {
			s.ports.Release(lease.UDPPort)
		}
	}
}

func (s *Server) watchContext() error {
	if s.ctxDone == nil {
		return nil
	}
	<-s.ctxDone
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.Shutdown(shutdownCtx)
}

func (s *Server) connectURL() string {
	base := strings.TrimRight(s.cfg.PortalURL, "/")
	if base == "" && s.apiListener != nil {
		return "https://" + HostPortOrLoopback(s.apiListener.Addr().String()) + types.PathSDKConnect
	}
	return base + types.PathSDKConnect
}

func (s *Server) quicPublicAddr() string {
	_, port, err := net.SplitHostPort(s.cfg.QUICListenAddr)
	if err != nil {
		port = "4017"
	}
	return net.JoinHostPort(s.cfg.RootHost, port)
}

func (s *Server) wrapAPIHandler(base http.Handler) http.Handler {
	if s.cfg.APIHandlerWrapper == nil {
		return base
	}
	return s.cfg.APIHandlerWrapper(base)
}

var (
	errLeaseNotFound    = errors.New("lease not found")
	errIPBanned         = errors.New("request denied because source IP is banned")
	errUnauthorized     = errors.New(types.APIErrorCodeUnauthorized)
	errHostnameConflict = errors.New("hostname already registered")
)

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, defaultControlBodyLimit)
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}

func normalizeHostnames(hosts []string) []string {
	seen := make(map[string]struct{}, len(hosts))
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = normalizeHostname(host)
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, host)
	}
	return out
}

func tokenMatches(expected, actual string) bool {
	if len(expected) == 0 || len(actual) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func writeAPIData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(types.APIEnvelope[any]{OK: true, Data: data})
}

func writeAPIOK(w http.ResponseWriter, status int) {
	writeAPIData(w, status, map[string]any{})
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(types.APIEnvelope[any]{
		OK:    false,
		Error: &types.APIError{Code: code, Message: message},
	})
}

func bridgeConns(left, right net.Conn) {
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

func (s *Server) context() context.Context {
	if s.baseContext != nil {
		if ctx := s.baseContext(); ctx != nil {
			return ctx
		}
	}
	return context.Background()
}

func (s *Server) isClosed() bool {
	if s.ctxDone == nil {
		return false
	}
	select {
	case <-s.ctxDone:
		return true
	default:
		return false
	}
}

func (s *Server) snapshotForLease(record *leaseRecord) LeaseSnapshot {
	if record == nil {
		return LeaseSnapshot{}
	}
	clientIP := record.ClientIP
	runtime := s.cfg.Policy
	return LeaseSnapshot{
		ID:          record.ID,
		Name:        record.Name,
		ClientIP:    clientIP,
		Transport:   record.Transport,
		Hostnames:   append([]string(nil), record.Hostnames...),
		Metadata:    record.Metadata,
		ExpiresAt:   record.ExpiresAt,
		FirstSeenAt: record.FirstSeenAt,
		LastSeenAt:  record.LastSeenAt,
		Ready:       record.Broker.ReadyCount(),
		UDPPort:     record.UDPPort,
		IsApproved:  runtime.EffectiveApproval(record.ID),
		IsBanned:    runtime.IsLeaseBanned(record.ID),
		IsDenied:    runtime.IsLeaseDenied(record.ID),
		IsIPBanned:  runtime.IPFilter().IsIPBanned(clientIP),
	}
}

func (s *Server) clientIPFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	return policy.ExtractClientIP(r, s.cfg.TrustProxyHeaders)
}

func (s *Server) isClientIPBanned(clientIP string) bool {
	return s.cfg.Policy.IPFilter().IsIPBanned(clientIP)
}

func (s *Server) isLeaseRoutable(record *leaseRecord) bool {
	if record == nil {
		return false
	}
	return s.cfg.Policy.IsLeaseRoutable(record.ID)
}

func (s *Server) touchLease(leaseID, clientIP string) {
	now := time.Now()

	s.mu.Lock()
	record := s.leases[strings.TrimSpace(leaseID)]
	if record != nil {
		record.LastSeenAt = now
		if strings.TrimSpace(clientIP) != "" {
			record.ClientIP = clientIP
		}
	}
	s.mu.Unlock()

	if record != nil && strings.TrimSpace(clientIP) != "" {
		s.cfg.Policy.IPFilter().RegisterLeaseIP(record.ID, clientIP)
	}
}

func (s *Server) startQUICTunnelListener(ctx context.Context) error {
	tlsCert, err := tls.X509KeyPair(s.cfg.APITLS.CertPEM, s.cfg.APITLS.KeyPEM)
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
		MaxIncomingStreams:  16,
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

// startUDPRelay starts a previously created UDP relay. Called after lock is released.
func (s *Server) startUDPRelay(leaseID string) {
	s.mu.RLock()
	relay, ok := s.udpRelays[leaseID]
	s.mu.RUnlock()
	if !ok || relay == nil {
		return
	}
	if err := relay.Start(s.context()); err != nil {
		log.Error().
			Err(err).
			Str("component", "relay-server").
			Str("lease_id", leaseID).
			Msg("failed to start udp relay")
	}
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
