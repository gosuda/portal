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

	"github.com/gosuda/keyless_tls/relay/l4"
	"golang.org/x/sync/errgroup"

	"github.com/gosuda/portal/v2/portal/acme"
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
)

type ServerConfig struct {
	PortalURL             string
	ACME                  acme.Config
	APIListenAddr         string
	SNIListenAddr         string
	TrustedProxyCIDRs     []*net.IPNet
	LeaseTTL              time.Duration
	ClaimTimeout          time.Duration
	IdleKeepaliveInterval time.Duration
	ReadyQueueLimit       int
	ClientHelloTimeout    time.Duration
	TrustProxyHeaders     bool
}

type Server struct {
	sniListener  net.Listener
	apiListener  net.Listener
	apiServer    *http.Server
	apiTLSClose  io.Closer
	acmeManager  *acme.Manager
	cancel       context.CancelFunc
	group        *errgroup.Group
	registry     *leaseRegistry
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
	rootHost := utils.PortalRootHost(cfg.PortalURL)
	if rootHost == "" {
		return nil, errors.New("root host is required")
	}
	return &Server{
		cfg:      cfg,
		rootHost: rootHost,
		registry: newLeaseRegistry(nil),
	}, nil
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
			lease.Broker.Close()
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

	record, ok := s.registry.Lookup(serverName)
	if !ok || time.Now().After(record.ExpiresAt) {
		_ = wrappedConn.Close()
		return
	}
	if !s.registry.policy.IsLeaseRoutable(record.ID) {
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
