package sdk

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

// UDPListenerConfig configures a standalone UDP listener that registers its own lease.
type UDPListenerConfig struct {
	Name           string
	ReverseToken   string
	Metadata       types.LeaseMetadata
	Transport      string // "udp" or "both", defaults to "udp"
	LeaseTTL       time.Duration
	RootCAPEM      []byte
	DialTimeout    time.Duration
	RequestTimeout time.Duration
}

// UDPListener manages a QUIC connection to the relay for a UDP-transport lease.
// It receives DATAGRAM frames from the relay and delivers decoded datagrams via
// the AcceptDatagram method.
type UDPListener struct {
	api         *apiClient
	baseContext func() context.Context
	ctxDone     <-chan struct{}
	cancel      context.CancelFunc

	name         string
	leaseID      string
	reverseToken string
	udpAddr      string
	quicAddr     string
	hostname     string
	metadata     types.LeaseMetadata
	leaseTTL     time.Duration

	conn      *quic.Conn
	datagrams chan UDPDatagram
	done      chan struct{}

	ownsLease bool
	closeOnce sync.Once
	mu        sync.Mutex
}

// UDPDatagram represents a single datagram received from a public client
// through the relay.
type UDPDatagram struct {
	FlowID  uint32
	Payload []byte
}

// AcceptDatagram blocks until a datagram is available or the listener is closed.
func (l *UDPListener) AcceptDatagram() (UDPDatagram, error) {
	select {
	case <-l.ctxDone:
		return UDPDatagram{}, net.ErrClosed
	case dg, ok := <-l.datagrams:
		if !ok {
			return UDPDatagram{}, net.ErrClosed
		}
		return dg, nil
	}
}

// SendDatagram sends a response datagram back to a client via the relay.
func (l *UDPListener) SendDatagram(flowID uint32, payload []byte) error {
	l.mu.Lock()
	conn := l.conn
	l.mu.Unlock()

	if conn == nil {
		return errors.New("quic connection not established")
	}
	return conn.SendDatagram(types.EncodeDatagram(flowID, payload))
}

// UDPAddr returns the public UDP address allocated by the relay.
func (l *UDPListener) UDPAddr() string {
	return l.udpAddr
}

// LeaseID returns the current lease ID.
func (l *UDPListener) LeaseID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.leaseID
}

// Hostname returns the hostname registered for this lease.
func (l *UDPListener) Hostname() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hostname
}

// Close tears down the QUIC connection. If this listener owns the lease
// (created via NewUDPListener), it also unregisters the lease. Attached listeners
// (created via Listener.AttachUDP) leave lease lifecycle to the TCP Listener.
func (l *UDPListener) Close() error {
	var closeErr error
	l.closeOnce.Do(func() {
		l.cancel()

		l.mu.Lock()
		conn := l.conn
		leaseID := l.leaseID
		l.mu.Unlock()

		if conn != nil {
			_ = conn.CloseWithError(0, "listener closed")
		}

		if l.ownsLease && l.api != nil && leaseID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			closeErr = l.api.unregisterLease(ctx, leaseID)
		}
	})
	return closeErr
}

// NewUDPListener registers a UDP-transport lease and returns a UDPListener.
func NewUDPListener(ctx context.Context, relayURL string, cfg UDPListenerConfig) (*UDPListener, error) {
	if strings.TrimSpace(cfg.Name) == "" {
		return nil, errors.New("listener name is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	reverseToken := strings.TrimSpace(cfg.ReverseToken)
	if reverseToken == "" {
		reverseToken = utils.RandomID("tok_")
	}
	leaseTTL := utils.DurationOrDefault(cfg.LeaseTTL, defaultLeaseTTL)

	transport := strings.ToLower(strings.TrimSpace(cfg.Transport))
	if transport == "" {
		transport = types.TransportUDP
	}

	api, err := newApiClient(relayURL, ListenerConfig{
		Name:           cfg.Name,
		ReverseToken:   reverseToken,
		Metadata:       cfg.Metadata,
		RootCAPEM:      cfg.RootCAPEM,
		DialTimeout:    cfg.DialTimeout,
		RequestTimeout: cfg.RequestTimeout,
	})
	if err != nil {
		return nil, err
	}

	if err := api.ensureReady(ctx); err != nil {
		api.close()
		return nil, err
	}

	registerResp, err := api.registerLease(ctx, leaseTTL, transport)
	if err != nil {
		api.close()
		return nil, err
	}

	listenerCtx, cancel := context.WithCancel(ctx)
	listener := &UDPListener{
		api:          api,
		baseContext:  func() context.Context { return listenerCtx },
		ctxDone:      listenerCtx.Done(),
		cancel:       cancel,
		name:         strings.TrimSpace(cfg.Name),
		leaseID:      registerResp.LeaseID,
		reverseToken: reverseToken,
		udpAddr:      registerResp.UDPAddr,
		quicAddr:     registerResp.QUICAddr,
		hostname:     registerResp.Hostname,
		metadata:     registerResp.Metadata,
		leaseTTL:     leaseTTL,
		datagrams:    make(chan UDPDatagram, 256),
		done:         make(chan struct{}),
		ownsLease:    true,
	}

	go listener.runSupervisor()
	go listener.runRenewLoop()

	return listener, nil
}

// AttachUDP creates a UDPListener that connects to an existing Listener's
// QUIC broker without registering a new lease or running a renew loop.
// The caller (the TCP Listener) owns the lease lifecycle.
// The Listener must have been registered with transport "udp" or "both".
func (l *Listener) AttachUDP(ctx context.Context) (*UDPListener, error) {
	// Wait for lease registration to complete before reading UDP addresses.
	if err := l.WaitRegistered(ctx); err != nil {
		return nil, fmt.Errorf("wait for registration: %w", err)
	}

	l.mu.Lock()
	leaseID := l.leaseID
	api := l.api
	udpAddr := l.udpAddr
	quicAddr := l.quicAddr
	l.mu.Unlock()

	if leaseID == "" {
		return nil, errors.New("lease not registered yet")
	}
	if api == nil {
		return nil, errors.New("api client not available")
	}
	if udpAddr == "" && quicAddr == "" {
		return nil, errors.New("lease does not have UDP transport enabled")
	}

	// Use context.Background — the caller's ctx may be a short-lived timeout
	// context (e.g. the 15-second waitCtx from Exposure.AttachUDP). The
	// UDPListener's lifecycle is managed by Close(), not context cancellation.
	listenerCtx, cancel := context.WithCancel(context.Background())
	udpL := &UDPListener{
		api:          api,
		baseContext:  func() context.Context { return listenerCtx },
		ctxDone:      listenerCtx.Done(),
		cancel:       cancel,
		leaseID:      leaseID,
		reverseToken: api.reverseToken,
		udpAddr:      udpAddr,
		quicAddr:     quicAddr,
		datagrams:    make(chan UDPDatagram, 256),
		done:         make(chan struct{}),
		ownsLease:    false,
	}

	go udpL.runSupervisor()
	return udpL, nil
}

func (l *UDPListener) runSupervisor() {
	for {
		select {
		case <-l.ctxDone:
			return
		default:
		}

		conn, err := l.api.openQUICSession(l.context(), l.quicAddr, l.leaseID, l.reverseToken)
		if err != nil {
			log.Warn().
				Err(err).
				Str("component", "sdk-udp-listener").
				Str("lease_id", l.leaseID).
				Msg("quic session open failed, retrying")
			utils.SleepOrDone(l.context(), 2*time.Second)
			continue
		}

		l.mu.Lock()
		l.conn = conn
		l.mu.Unlock()

		log.Info().
			Str("component", "sdk-udp-listener").
			Str("lease_id", l.leaseID).
			Str("remote_addr", conn.RemoteAddr().String()).
			Msg("quic tunnel connected")

		l.receiveLoop(conn)

		l.mu.Lock()
		if l.conn == conn {
			l.conn = nil
		}
		l.mu.Unlock()

		if l.isClosed() {
			return
		}
		utils.SleepOrDone(l.context(), time.Second)
	}
}

func (l *UDPListener) receiveLoop(conn *quic.Conn) {
	for {
		data, err := conn.ReceiveDatagram(l.context())
		if err != nil {
			if !l.isClosed() {
				log.Warn().
					Err(err).
					Str("component", "sdk-udp-listener").
					Str("lease_id", l.leaseID).
					Msg("quic receive loop ended")
			}
			return
		}

		frame, err := types.DecodeDatagram(data)
		if err != nil {
			continue
		}

		select {
		case l.datagrams <- UDPDatagram{FlowID: frame.FlowID, Payload: frame.Payload}:
		case <-l.ctxDone:
			return
		}
	}
}

func (l *UDPListener) runRenewLoop() {
	interval := l.leaseTTL / 2
	if interval <= 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-l.ctxDone:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(l.context(), 10*time.Second)
			err := l.api.renewLease(ctx, l.leaseID, l.leaseTTL)
			cancel()
			if err != nil {
				log.Warn().
					Err(err).
					Str("component", "sdk-udp-listener").
					Str("lease_id", l.leaseID).
					Msg("lease renewal failed")
			}
		}
	}
}

func (l *UDPListener) context() context.Context {
	if l.baseContext != nil {
		if ctx := l.baseContext(); ctx != nil {
			return ctx
		}
	}
	return context.Background()
}

func (l *UDPListener) isClosed() bool {
	if l.ctxDone == nil {
		return false
	}
	select {
	case <-l.ctxDone:
		return true
	default:
		return false
	}
}

