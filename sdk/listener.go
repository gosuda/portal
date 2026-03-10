package sdk

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/types"
)

type ListenerConfig struct {
	Name             string
	ReverseToken     string
	Hostnames        []string
	Metadata         types.LeaseMetadata
	RootCAPEM        []byte
	DialTimeout      time.Duration
	RequestTimeout   time.Duration
	HandshakeTimeout time.Duration
	LeaseTTL         time.Duration
	RenewBefore      time.Duration
	ReadyTarget      int
}

type Listener struct {
	tlsCloser        io.Closer
	tlsConfig        *tls.Config
	readyTarget      int
	leaseTTL         time.Duration
	renewBefore      time.Duration
	handshakeTimeout time.Duration
	ctx              context.Context
	cancel           context.CancelFunc
	api              *relayClient
	signal           chan struct{}
	accepted         chan net.Conn
	leaseID          string
	hostnames        []string
	metadata         types.LeaseMetadata

	activeSessions int
	closeOnce      sync.Once
	mu             sync.Mutex
}

// NewListener creates one relay listener and its dedicated relay transport for one relay URL.
func NewListener(ctx context.Context, relayURL string, cfg ListenerConfig) (*Listener, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	listenerCtx, cancel := context.WithCancel(ctx)
	readyTarget := cfg.ReadyTarget
	if readyTarget <= 0 {
		readyTarget = defaultReadyTarget
	}
	leaseTTL := cfg.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = defaultLeaseTTL
	}
	handshakeTimeout := cfg.HandshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = defaultHandshakeTimeout
	}
	renewBefore := cfg.RenewBefore
	if renewBefore <= 0 {
		renewBefore = defaultRenewBefore
	}

	api, err := newRelayClient(listenerCtx, relayURL, cfg)
	if err != nil {
		cancel()
		return nil, err
	}

	l := &Listener{
		ctx:              listenerCtx,
		cancel:           cancel,
		api:              api,
		signal:           make(chan struct{}, 1),
		accepted:         make(chan net.Conn, max(readyTarget*2, 1)),
		readyTarget:      readyTarget,
		leaseTTL:         leaseTTL,
		renewBefore:      renewBefore,
		handshakeTimeout: handshakeTimeout,
	}

	resp, err := api.registerLease(listenerCtx, cfg.Hostnames, leaseTTL)
	if err != nil {
		api.close()
		cancel()
		return nil, err
	}

	tlsConf, tlsCloser, err := keyless.BuildClientTLSConfig(api.baseURL.String(), resp.Hostnames)
	if err != nil {
		_ = api.unregisterLease(context.Background(), resp.LeaseID)
		api.close()
		cancel()
		return nil, err
	}

	if listenerCtx.Err() != nil {
		_ = api.unregisterLease(context.Background(), resp.LeaseID)
		_ = tlsCloser.Close()
		api.close()
		cancel()
		return nil, listenerCtx.Err()
	}

	l.mu.Lock()
	l.leaseID = resp.LeaseID
	l.hostnames = append([]string(nil), resp.Hostnames...)
	l.metadata = cloneMetadata(resp.Metadata)
	l.tlsConfig = tlsConf
	l.tlsCloser = tlsCloser
	l.mu.Unlock()

	go l.runSupervisor()
	go l.runRenewLoop()
	l.notify()
	return l, nil
}

func (l *Listener) Accept() (net.Conn, error) {
	select {
	case <-l.ctx.Done():
		return nil, net.ErrClosed
	case conn := <-l.accepted:
		if conn == nil {
			return nil, net.ErrClosed
		}
		return conn, nil
	}
}

func (l *Listener) Close() error {
	var closeErr error
	l.closeOnce.Do(func() {
		if l.cancel != nil {
			l.cancel()
		}

		l.mu.Lock()
		leaseID := l.leaseID
		tlsCloser := l.tlsCloser
		api := l.api
		l.leaseID = ""
		l.tlsConfig = nil
		l.tlsCloser = nil
		l.activeSessions = 0
		l.mu.Unlock()

		l.drainAccepted()

		if api != nil && leaseID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			closeErr = errors.Join(closeErr, api.unregisterLease(ctx, leaseID))
			cancel()
		}
		if tlsCloser != nil {
			closeErr = errors.Join(closeErr, tlsCloser.Close())
		}
		if api != nil {
			api.close()
		}
	})
	return closeErr
}

func (l *Listener) Addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.leaseID == "" {
		return listenerAddr("portal:pending")
	}
	return listenerAddr("portal:" + l.leaseID)
}

func (l *Listener) LeaseID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.leaseID
}

func (l *Listener) Hostnames() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.hostnames...)
}

func (l *Listener) Metadata() types.LeaseMetadata {
	l.mu.Lock()
	defer l.mu.Unlock()
	return cloneMetadata(l.metadata)
}

func (l *Listener) PublicURLs() []string {
	l.mu.Lock()
	hostnames := append([]string(nil), l.hostnames...)
	l.mu.Unlock()

	urls := make([]string, 0, len(hostnames))
	for _, host := range hostnames {
		urls = append(urls, "https://"+host)
	}
	return urls
}

func (l *Listener) runSupervisor() {
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-l.signal:
		}

		for l.reserveSessionSlot() {
			go l.runSession()
		}
	}
}

func (l *Listener) runRenewLoop() {
	interval := l.leaseTTL / 2
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if l.renewBefore > 0 && l.leaseTTL > l.renewBefore {
		interval = l.leaseTTL - l.renewBefore
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var consecutiveFailures int

	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			l.mu.Lock()
			leaseID := l.leaseID
			l.mu.Unlock()

			ctx, cancel := context.WithTimeout(l.context(), 10*time.Second)
			err := l.api.renewLease(ctx, leaseID, l.leaseTTL)
			cancel()

			if err != nil {
				if isLeaseNotFound(err) {
					log.Warn().
						Str("component", "sdk-listener").
						Str("lease_id", leaseID).
						Msg("lease not found on relay, attempting re-registration")
					if reregErr := l.reregister(); reregErr != nil {
						log.Error().Err(reregErr).
							Str("component", "sdk-listener").
							Msg("lease re-registration failed")
					} else {
						consecutiveFailures = 0
						log.Info().
							Str("component", "sdk-listener").
							Str("lease_id", l.LeaseID()).
							Strs("hostnames", l.Hostnames()).
							Msg("lease re-registered successfully")
						continue
					}
				}

				consecutiveFailures++
				event := log.Warn()
				if consecutiveFailures >= 3 {
					event = log.Error()
				}
				event.Err(err).
					Str("component", "sdk-listener").
					Str("lease_id", l.LeaseID()).
					Int("consecutive_failures", consecutiveFailures).
					Msg("lease renewal failed")
			} else {
				consecutiveFailures = 0
			}
		}
	}
}

func (l *Listener) runSession() {
	defer l.releaseSessionSlot()

	sessionCtx := l.context()
	l.mu.Lock()
	leaseID := l.leaseID
	l.mu.Unlock()

	conn, err := l.api.openReverseSession(sessionCtx, leaseID)
	if err != nil {
		sleepOrDone(sessionCtx, time.Second)
		return
	}

	claimed, err := l.awaitActivation(conn)
	if err != nil {
		_ = conn.Close()
		if !claimed && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
			sleepOrDone(sessionCtx, time.Second)
		}
	}
}

func (l *Listener) awaitActivation(conn net.Conn) (bool, error) {
	var marker [1]byte
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * l.handshakeTimeout))
		if _, err := io.ReadFull(conn, marker[:]); err != nil {
			return false, err
		}
		_ = conn.SetReadDeadline(time.Time{})

		switch marker[0] {
		case types.MarkerKeepalive:
			continue
		case types.MarkerTLSStart:
			if err := l.activate(conn); err != nil {
				return true, err
			}
			return true, nil
		default:
			return false, fmt.Errorf("unexpected reverse marker: 0x%02x", marker[0])
		}
	}
}

func (l *Listener) activate(conn net.Conn) error {
	l.mu.Lock()
	tlsCfg := l.tlsConfig
	l.mu.Unlock()

	tlsConn := tls.Server(conn, tlsCfg)
	handshakeCtx, cancel := context.WithTimeout(l.context(), l.handshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		return err
	}

	select {
	case <-l.ctx.Done():
		_ = tlsConn.Close()
		return l.context().Err()
	case l.accepted <- tlsConn:
		return nil
	}
}

func (l *Listener) reregister() error {
	ctx, cancel := context.WithTimeout(l.context(), 10*time.Second)
	defer cancel()

	l.mu.Lock()
	hostnames := append([]string(nil), l.hostnames...)
	l.mu.Unlock()

	resp, err := l.api.registerLease(ctx, hostnames, l.leaseTTL)
	if err != nil {
		return err
	}

	tlsConf, tlsCloser, err := keyless.BuildClientTLSConfig(l.api.baseURL.String(), resp.Hostnames)
	if err != nil {
		_ = l.api.unregisterLease(ctx, resp.LeaseID)
		return err
	}

	if l.isClosed() {
		_ = l.api.unregisterLease(context.Background(), resp.LeaseID)
		_ = tlsCloser.Close()
		return context.Canceled
	}

	l.mu.Lock()
	oldCloser := l.tlsCloser
	l.leaseID = resp.LeaseID
	l.hostnames = append([]string(nil), resp.Hostnames...)
	l.metadata = cloneMetadata(resp.Metadata)
	l.tlsConfig = tlsConf
	l.tlsCloser = tlsCloser
	l.mu.Unlock()

	if oldCloser != nil {
		_ = oldCloser.Close()
	}

	l.notify()
	return nil
}

func isLeaseNotFound(err error) bool {
	return errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeLeaseNotFound})
}

func (l *Listener) reserveSessionSlot() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.isClosed() {
		return false
	}
	if l.activeSessions >= l.readyTarget {
		return false
	}
	l.activeSessions++
	return true
}

func (l *Listener) releaseSessionSlot() {
	l.mu.Lock()
	if l.activeSessions > 0 {
		l.activeSessions--
	}
	l.mu.Unlock()
	l.notify()
}

func (l *Listener) notify() {
	select {
	case l.signal <- struct{}{}:
	default:
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

type listenerAddr string

func (a listenerAddr) Network() string { return "portal" }
func (a listenerAddr) String() string  { return string(a) }

func (l *Listener) context() context.Context {
	if l.ctx != nil {
		return l.ctx
	}
	return context.Background()
}

func (l *Listener) isClosed() bool {
	if l.ctx == nil {
		return false
	}
	select {
	case <-l.ctx.Done():
		return true
	default:
		return false
	}
}

func (l *Listener) drainAccepted() {
	for {
		select {
		case conn := <-l.accepted:
			if conn != nil {
				_ = conn.Close()
			}
		default:
			return
		}
	}
}
