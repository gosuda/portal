package sdk

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/types"
)

const (
	defaultListenerRetryDelay = 1 * time.Second
)

type listenerState uint8

const (
	listenerStatePending listenerState = iota
	listenerStateReady
	listenerStateClosed
)

type ListenerConfig struct {
	Name         string
	ReverseToken string
	Hostnames    []string
	Metadata     types.LeaseMetadata
	RootCAPEM    []byte
	RetryCount   int
}

type Listener struct {
	ctx      context.Context
	cancel   context.CancelFunc
	accepted chan net.Conn
	refill   chan struct{}

	readyTarget      int
	leaseTTL         time.Duration
	renewInterval    time.Duration
	handshakeTimeout time.Duration
	retryCount       int
	retryDelay       time.Duration

	mu              sync.Mutex
	api             *relayClient
	leaseID         string
	hostnames       []string
	tlsConfig       *tls.Config
	tlsCloser       io.Closer
	activeSessions  int
	sessionFailures int
	state           listenerState

	closeOnce sync.Once
	closeErr  error
}

// NewListener creates one relay listener and its dedicated relay transport for one relay URL.
func NewListener(ctx context.Context, relayURL string, cfg ListenerConfig) (*Listener, error) {
	api, err := newRelayClient(relayURL, cfg)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	readyTarget := defaultReadyTarget
	leaseTTL := defaultLeaseTTL
	handshakeTimeout := defaultHandshakeTimeout
	renewBefore := defaultRenewBefore
	renewInterval := leaseTTL / 2
	if leaseTTL > renewBefore {
		renewInterval = leaseTTL - renewBefore
	}
	if renewInterval <= 0 {
		renewInterval = leaseTTL / 2
	}
	if renewInterval <= 0 {
		renewInterval = time.Second
	}

	retryCount := cfg.RetryCount
	retryDelay := defaultListenerRetryDelay
	if retryCount < 0 {
		retryCount = 0
	}
	if retryDelay <= 0 {
		retryDelay = defaultListenerRetryDelay
	}

	listenerCtx, cancel := context.WithCancel(ctx)
	l := &Listener{
		ctx:              listenerCtx,
		cancel:           cancel,
		accepted:         make(chan net.Conn, max(readyTarget*2, 1)),
		refill:           make(chan struct{}, 1),
		readyTarget:      readyTarget,
		leaseTTL:         leaseTTL,
		renewInterval:    renewInterval,
		handshakeTimeout: handshakeTimeout,
		retryCount:       retryCount,
		retryDelay:       retryDelay,
		api:              api,
		state:            listenerStatePending,
	}

	go l.run(listenerCtx)
	return l, nil
}

func (l *Listener) Accept() (net.Conn, error) {
	select {
	case <-l.ctx.Done():
		select {
		case conn := <-l.accepted:
			if conn != nil {
				_ = conn.Close()
			}
		default:
		}
		return nil, net.ErrClosed
	case conn := <-l.accepted:
		if conn == nil {
			return nil, net.ErrClosed
		}
		select {
		case <-l.ctx.Done():
			_ = conn.Close()
			return nil, net.ErrClosed
		default:
			return conn, nil
		}
	}
}

func (l *Listener) Close() error {
	l.closeOnce.Do(func() {
		l.closeErr = l.shutdown()
	})
	return l.closeErr
}

func (l *Listener) Addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()

	if strings.TrimSpace(l.leaseID) == "" {
		return listenerAddr("portal:pending")
	}
	return listenerAddr("portal:" + l.leaseID)
}

func (l *Listener) run(runCtx context.Context) {
	logger := log.With().
		Str("component", "sdk-listener").
		Str("name", l.api.name).
		Logger()

	var err error
	for attempt := 1; ; attempt++ {
		err = l.establish(runCtx)
		if err == nil {
			l.mu.Lock()
			if l.state == listenerStateClosed {
				l.mu.Unlock()
				return
			}
			leaseID := l.leaseID
			hostnames := append([]string(nil), l.hostnames...)
			l.mu.Unlock()

			logger.Info().
				Str("lease_id", leaseID).
				Strs("hostnames", hostnames).
				Msg("listener connected")

			go l.runSessionPool(runCtx)
			go l.runRenewLoop(runCtx)
			l.signalRefill()
			return
		}

		if errors.Is(runCtx.Err(), context.Canceled) {
			return
		}

		logger.Warn().
			Err(err).
			Int("attempt", attempt).
			Dur("retry_in", l.retryDelay).
			Msg("listener bootstrap failed")

		if l.retryLimitReached(attempt) {
			break
		}
		if !sleepOrDone(runCtx, l.retryDelay) {
			return
		}
	}

	l.fail(err, "listener bootstrap retry limit reached")
}

func (l *Listener) establish(runCtx context.Context) error {
	l.mu.Lock()
	if l.state == listenerStateClosed {
		l.mu.Unlock()
		return context.Canceled
	}
	api := l.api
	l.mu.Unlock()

	resp, err := api.registerLease(runCtx, nil, l.leaseTTL)
	if err != nil {
		return err
	}

	tlsConfig, tlsCloser, err := keyless.BuildClientTLSConfig(api.baseURL.String(), resp.Hostnames)
	if err != nil {
		_ = api.unregisterLease(context.Background(), resp.LeaseID)
		return err
	}

	l.mu.Lock()
	if l.state == listenerStateClosed || runCtx.Err() != nil {
		l.mu.Unlock()
		_ = api.unregisterLease(context.Background(), resp.LeaseID)
		_ = tlsCloser.Close()
		return context.Canceled
	}
	oldCloser := l.tlsCloser
	l.leaseID = resp.LeaseID
	l.hostnames = append([]string(nil), resp.Hostnames...)
	l.tlsConfig = tlsConfig
	l.tlsCloser = tlsCloser
	l.activeSessions = 0
	l.sessionFailures = 0
	l.state = listenerStateReady
	l.mu.Unlock()

	if oldCloser != nil {
		_ = oldCloser.Close()
	}
	return nil
}

func (l *Listener) runSessionPool(runCtx context.Context) {
	for {
		select {
		case <-runCtx.Done():
			return
		case <-l.refill:
		}

		for {
			l.mu.Lock()
			ready := l.state == listenerStateReady &&
				l.api != nil &&
				strings.TrimSpace(l.leaseID) != "" &&
				l.tlsConfig != nil &&
				l.activeSessions < l.readyTarget
			if !ready {
				l.mu.Unlock()
				break
			}
			l.activeSessions++
			l.mu.Unlock()

			go l.runSession(runCtx)
		}
	}
}

func (l *Listener) runRenewLoop(runCtx context.Context) {
	failures := 0
	wait := l.renewInterval

	for {
		if !sleepOrDone(runCtx, wait) {
			return
		}

		l.mu.Lock()
		api := l.api
		leaseID := l.leaseID
		ready := l.state == listenerStateReady
		l.mu.Unlock()

		if !ready || api == nil || strings.TrimSpace(leaseID) == "" {
			wait = l.renewInterval
			continue
		}

		ctx, cancel := context.WithTimeout(runCtx, 10*time.Second)
		err := api.renewLease(ctx, leaseID, l.leaseTTL)
		cancel()

		if err == nil {
			failures = 0
			wait = l.renewInterval
			continue
		}

		if isLeaseNotFound(err) {
			log.Warn().
				Err(err).
				Str("component", "sdk-listener").
				Str("lease_id", leaseID).
				Msg("lease not found on relay, attempting re-registration")

			if reregErr := l.reregister(runCtx); reregErr == nil {
				failures = 0
				wait = l.renewInterval

				l.mu.Lock()
				newLeaseID := l.leaseID
				newHostnames := append([]string(nil), l.hostnames...)
				l.mu.Unlock()

				log.Info().
					Str("component", "sdk-listener").
					Str("lease_id", newLeaseID).
					Strs("hostnames", newHostnames).
					Msg("lease re-registered successfully")
				continue
			} else {
				err = reregErr
				log.Error().
					Err(reregErr).
					Str("component", "sdk-listener").
					Str("lease_id", leaseID).
					Msg("lease re-registration failed")
			}
		}

		failures++
		event := log.Warn()
		if l.retryLimitReached(failures) {
			event = log.Error()
		}
		event.Err(err).
			Str("component", "sdk-listener").
			Str("lease_id", leaseID).
			Int("consecutive_failures", failures).
			Msg("lease renewal failed")

		if l.retryLimitReached(failures) {
			l.fail(err, "listener renew retry limit reached")
			return
		}
		wait = l.retryDelay
	}
}

func (l *Listener) runSession(runCtx context.Context) {
	defer func() {
		l.mu.Lock()
		if l.activeSessions > 0 {
			l.activeSessions--
		}
		l.mu.Unlock()
		l.signalRefill()
	}()

	fail := func(err error) {
		if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
			return
		}

		l.mu.Lock()
		if l.state == listenerStateClosed {
			l.mu.Unlock()
			return
		}
		l.sessionFailures++
		failures := l.sessionFailures
		l.mu.Unlock()

		if l.retryLimitReached(failures) {
			l.fail(err, "listener session retry limit reached")
			return
		}

		_ = sleepOrDone(runCtx, l.retryDelay)
	}

	l.mu.Lock()
	ready := l.state == listenerStateReady
	api := l.api
	leaseID := l.leaseID
	tlsConfig := l.tlsConfig
	l.mu.Unlock()
	if !ready || api == nil || strings.TrimSpace(leaseID) == "" || tlsConfig == nil {
		return
	}

	conn, err := api.openReverseSession(runCtx, leaseID)
	if err != nil {
		fail(err)
		return
	}

	var marker [1]byte
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * l.handshakeTimeout))
		if _, err := io.ReadFull(conn, marker[:]); err != nil {
			_ = conn.Close()
			fail(err)
			return
		}
		_ = conn.SetReadDeadline(time.Time{})

		switch marker[0] {
		case types.MarkerKeepalive:
			continue
		case types.MarkerTLSStart:
			tlsConn := tls.Server(conn, tlsConfig)
			handshakeCtx, cancel := context.WithTimeout(runCtx, l.handshakeTimeout)
			err := tlsConn.HandshakeContext(handshakeCtx)
			cancel()
			if err != nil {
				_ = tlsConn.Close()
				fail(err)
				return
			}

			l.mu.Lock()
			if l.state != listenerStateClosed {
				l.sessionFailures = 0
			}
			l.mu.Unlock()

			select {
			case <-runCtx.Done():
				_ = tlsConn.Close()
			case l.accepted <- tlsConn:
			}
			return
		default:
			_ = conn.Close()
			fail(errors.New("unexpected reverse marker"))
			return
		}
	}
}

func (l *Listener) publicURLs() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != listenerStateReady || len(l.hostnames) == 0 {
		return nil
	}

	urls := make([]string, 0, len(l.hostnames))
	for _, host := range l.hostnames {
		urls = append(urls, "https://"+host)
	}
	return urls
}

func (l *Listener) reregister(runCtx context.Context) error {
	l.mu.Lock()
	if l.state == listenerStateClosed {
		l.mu.Unlock()
		return context.Canceled
	}
	api := l.api
	hostnames := append([]string(nil), l.hostnames...)
	l.mu.Unlock()

	ctx, cancel := context.WithTimeout(runCtx, 10*time.Second)
	defer cancel()

	resp, err := api.registerLease(ctx, hostnames, l.leaseTTL)
	if err != nil {
		return err
	}

	tlsConfig, tlsCloser, err := keyless.BuildClientTLSConfig(api.baseURL.String(), resp.Hostnames)
	if err != nil {
		_ = api.unregisterLease(context.Background(), resp.LeaseID)
		return err
	}

	l.mu.Lock()
	if l.state == listenerStateClosed || runCtx.Err() != nil {
		l.mu.Unlock()
		_ = api.unregisterLease(context.Background(), resp.LeaseID)
		_ = tlsCloser.Close()
		return context.Canceled
	}
	oldCloser := l.tlsCloser
	l.leaseID = resp.LeaseID
	l.hostnames = append([]string(nil), resp.Hostnames...)
	l.tlsConfig = tlsConfig
	l.tlsCloser = tlsCloser
	l.sessionFailures = 0
	l.state = listenerStateReady
	l.mu.Unlock()

	if oldCloser != nil {
		_ = oldCloser.Close()
	}
	l.signalRefill()
	return nil
}

func (l *Listener) shutdown() error {
	l.mu.Lock()
	if l.state == listenerStateClosed {
		l.mu.Unlock()
		return nil
	}
	l.state = listenerStateClosed
	cancel := l.cancel
	api := l.api
	leaseID := l.leaseID
	tlsCloser := l.tlsCloser
	l.leaseID = ""
	l.tlsConfig = nil
	l.tlsCloser = nil
	l.activeSessions = 0
	l.sessionFailures = 0
	l.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	l.drainAccepted()

	var closeErr error
	if api != nil && strings.TrimSpace(leaseID) != "" {
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
	return closeErr
}

func (l *Listener) fail(err error, message string) {
	closed := false
	l.closeOnce.Do(func() {
		closed = true
		l.closeErr = errors.Join(err, l.shutdown())
	})
	if !closed {
		return
	}

	log.Error().
		Str("component", "sdk-listener").
		Str("name", l.api.name).
		Err(l.closeErr).
		Msg(message)
}

func (l *Listener) signalRefill() {
	select {
	case l.refill <- struct{}{}:
	default:
	}
}

func (l *Listener) retryLimitReached(failures int) bool {
	return l.retryCount > 0 && failures >= l.retryCount
}

func isLeaseNotFound(err error) bool {
	return errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeLeaseNotFound})
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
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

type listenerAddr string

func (a listenerAddr) Network() string { return "portal" }
func (a listenerAddr) String() string  { return string(a) }
