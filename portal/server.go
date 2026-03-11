package portal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/keyless_tls/relay/l4"
	"golang.org/x/sync/errgroup"

	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/types"
)

const (
	defaultLeaseTTL          = 30 * time.Second
	defaultClaimTimeout      = 10 * time.Second
	defaultIdleKeepalive     = 15 * time.Second
	defaultReadyQueueLimit   = 8
	defaultClientHelloWait   = 2 * time.Second
	defaultControlBodyLimit  = 4 << 20
	defaultSessionWriteLimit = 5 * time.Second
)

type ServerConfig struct {
	APIHandlerWrapper     func(http.Handler) http.Handler
	KeylessSignerHandler  http.Handler
	Policy                *policy.Runtime
	PortalURL             string
	APIListenAddr         string
	SNIListenAddr         string
	RootHost              string
	RootFallbackAddr      string
	APITLS                keyless.TLSMaterialConfig
	LeaseTTL              time.Duration
	ClaimTimeout          time.Duration
	IdleKeepaliveInterval time.Duration
	ReadyQueueLimit       int
	ClientHelloTimeout    time.Duration
	TrustProxyHeaders     bool
}

type Server struct {
	sniListener  net.Listener
	apiTLSClose  io.Closer
	apiListener  net.Listener
	apiServer    *http.Server
	cancel       context.CancelFunc
	group        *errgroup.Group
	routes       *routeTable
	leases       map[string]*leaseRecord
	cfg          ServerConfig
	mu           sync.RWMutex
	shutdownOnce sync.Once
}

type leaseRecord struct {
	ExpiresAt    time.Time
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	Broker       *leaseBroker
	ID           string
	Name         string
	ReverseToken string
	ClientIP     string
	Hostnames    []string
	Metadata     types.LeaseMetadata
}

type LeaseSnapshot struct {
	ExpiresAt   time.Time
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	ID          string
	Name        string
	ClientIP    string
	Hostnames   []string
	Metadata    types.LeaseMetadata
	Ready       int
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

	return &Server{
		cfg:    cfg,
		routes: newRouteTable(),
		leases: make(map[string]*leaseRecord),
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

	wrappedAPIListener, apiServer, apiCloser, err := s.newAPIServer(apiListener)
	if err != nil {
		_ = apiListener.Close()
		_ = sniListener.Close()
		cancel()
		return err
	}

	s.apiListener = wrappedAPIListener
	s.sniListener = sniListener
	s.apiServer = apiServer
	s.apiTLSClose = apiCloser
	s.cancel = cancel
	s.group = group

	group.Go(s.runAPIServer)
	group.Go(func() error { return s.runSNIListener(groupCtx) })
	group.Go(func() error { return s.runLeaseJanitor(groupCtx) })
	group.Go(func() error { return s.watchContext(groupCtx) })
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
			lease.Broker.Close()
		}
		s.mu.Unlock()

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

func (s *Server) runSNIListener(ctx context.Context) error {
	for {
		conn, err := s.sniListener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handleSNIConn(ctx, conn)
	}
}

func (s *Server) handleSNIConn(ctx context.Context, conn net.Conn) {
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
		s.bridgeToFallback(ctx, wrappedConn)
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

	claimCtx, cancel := context.WithTimeout(ctx, s.cfg.ClaimTimeout)
	defer cancel()

	session, err := record.Broker.Claim(claimCtx)
	if err != nil {
		_ = wrappedConn.Close()
		return
	}

	bridgeConns(wrappedConn, session.Conn())
	_ = session.Close()
}

func (s *Server) bridgeToFallback(ctx context.Context, conn net.Conn) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	upstream, err := dialer.DialContext(ctx, "tcp", HostPortOrLoopback(s.cfg.RootFallbackAddr))
	if err != nil {
		_ = conn.Close()
		return
	}
	bridgeConns(conn, upstream)
}

func (s *Server) runLeaseJanitor(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
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
		lease.Broker.Close()
	}
}

func (s *Server) watchContext(ctx context.Context) error {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.Shutdown(shutdownCtx)
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
		Hostnames:   append([]string(nil), record.Hostnames...),
		Metadata:    record.Metadata,
		ExpiresAt:   record.ExpiresAt,
		FirstSeenAt: record.FirstSeenAt,
		LastSeenAt:  record.LastSeenAt,
		Ready:       record.Broker.ReadyCount(),
		IsApproved:  runtime.EffectiveApproval(record.ID),
		IsBanned:    runtime.IsLeaseBanned(record.ID),
		IsDenied:    runtime.IsLeaseDenied(record.ID),
		IsIPBanned:  runtime.IPFilter().IsIPBanned(clientIP),
	}
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
