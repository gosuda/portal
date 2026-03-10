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
	defaultListenerRetryCount = 30
	defaultListenerRetryDelay = time.Second
)

type listenerState uint8

const (
	listenerStatePending listenerState = iota
	listenerStateReady
	listenerStateStale
	listenerStateClosed
)

type ListenRequest struct {
	Name         string
	ReverseToken string
	Hostnames    []string
	Metadata     types.LeaseMetadata
	ReadyTarget  int
	LeaseTTL     time.Duration
}

type listenerOptions struct {
	client             *RelayClient
	handshakeTimeout   time.Duration
	renewBefore        time.Duration
	retryCount         int
	retryDelay         time.Duration
	defaultLeaseTTL    time.Duration
	defaultReadyTarget int
}

type Listener struct {
	ctx      context.Context
	cancel   context.CancelFunc
	accepted chan net.Conn
	refill   chan struct{}

	name             string
	reverseToken     string
	metadata         types.LeaseMetadata
	readyTarget      int
	leaseTTL         time.Duration
	renewInterval    time.Duration
	handshakeTimeout time.Duration
	retryCount       int
	retryDelay       time.Duration

	mu              sync.Mutex
	client          *RelayClient
	leaseID         string
	hostnames       []string
	tlsConfig       *tls.Config
	tlsCloser       io.Closer
	activeSessions  int
	sessionFailures int
	state           listenerState
	runID           uint64

	closeOnce sync.Once
	closeErr  error
}

func NewListener(ctx context.Context, req ListenRequest, opts listenerOptions) (*Listener, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("listener name is required")
	}
	if opts.client == nil {
		return nil, errors.New("listener client is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	reverseToken := strings.TrimSpace(req.ReverseToken)
	if reverseToken == "" {
		reverseToken = randomToken()
	}

	readyTarget := req.ReadyTarget
	if readyTarget <= 0 {
		readyTarget = opts.defaultReadyTarget
	}
	if readyTarget <= 0 {
		readyTarget = defaultReadyTarget
	}

	leaseTTL := req.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = opts.defaultLeaseTTL
	}
	if leaseTTL <= 0 {
		leaseTTL = defaultLeaseTTL
	}

	handshakeTimeout := opts.handshakeTimeout
	if handshakeTimeout <= 0 {
		handshakeTimeout = defaultHandshakeTimeout
	}

	renewBefore := opts.renewBefore
	if renewBefore <= 0 {
		renewBefore = defaultRenewBefore
	}
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

	retryCount := opts.retryCount
	if retryCount <= 0 {
		retryCount = defaultListenerRetryCount
	}
	retryDelay := opts.retryDelay
	if retryDelay <= 0 {
		retryDelay = defaultListenerRetryDelay
	}

	listenerCtx, cancel := context.WithCancel(ctx)
	l := &Listener{
		ctx:              listenerCtx,
		cancel:           cancel,
		accepted:         make(chan net.Conn, max(readyTarget*2, 1)),
		refill:           make(chan struct{}, 1),
		name:             strings.TrimSpace(req.Name),
		reverseToken:     reverseToken,
		metadata:         cloneMetadata(req.Metadata),
		readyTarget:      readyTarget,
		leaseTTL:         leaseTTL,
		renewInterval:    renewInterval,
		handshakeTimeout: handshakeTimeout,
		retryCount:       retryCount,
		retryDelay:       retryDelay,
		client:           opts.client,
		hostnames:        append([]string(nil), req.Hostnames...),
		state:            listenerStatePending,
		runID:            1,
	}

	go l.run(listenerCtx, l.runID)
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
	var closeErr error
	l.closeOnce.Do(func() {
		closeErr = l.closeCurrent()
	})
	return closeErr
}

func (l *Listener) Addr() net.Addr {
	l.mu.Lock()
	defer l.mu.Unlock()

	if strings.TrimSpace(l.leaseID) == "" {
		return listenerAddr("portal:pending")
	}
	return listenerAddr("portal:" + l.leaseID)
}

func (l *Listener) Reactivate(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	l.mu.Lock()
	if l.state == listenerStateClosed {
		l.mu.Unlock()
		return net.ErrClosed
	}
	if l.state != listenerStateStale {
		l.mu.Unlock()
		return errors.New("listener is not stale")
	}

	runCtx, cancel := context.WithCancel(ctx)
	l.ctx = runCtx
	l.cancel = cancel
	l.state = listenerStatePending
	l.activeSessions = 0
	l.sessionFailures = 0
	l.runID++
	runID := l.runID
	l.mu.Unlock()

	l.drainAccepted()
	go l.run(runCtx, runID)
	return nil
}

func (l *Listener) run(runCtx context.Context, runID uint64) {
	logger := log.With().
		Str("component", "sdk-listener").
		Str("name", l.name).
		Logger()

	var err error
	for attempt := 1; attempt <= l.retryCount; attempt++ {
		err = l.establish(runID, runCtx)
		if err == nil {
			l.mu.Lock()
			if l.runID != runID {
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

			go l.runSessionPool(runCtx, runID)
			go l.runRenewLoop(runCtx, runID)
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

		if attempt == l.retryCount {
			break
		}
		if !sleepOrDone(runCtx, l.retryDelay) {
			return
		}
	}

	l.fail(runID, err, "listener bootstrap retry limit reached")
}

func (l *Listener) establish(runID uint64, runCtx context.Context) error {
	l.mu.Lock()
	if l.runID != runID {
		l.mu.Unlock()
		return context.Canceled
	}
	client := l.client
	hostnames := append([]string(nil), l.hostnames...)
	l.mu.Unlock()

	resp, err := client.registerLease(runCtx, types.RegisterRequest{
		Name:         l.name,
		Hostnames:    hostnames,
		Metadata:     cloneMetadata(l.metadata),
		ReverseToken: l.reverseToken,
		TLS:          true,
		TTLSeconds:   int(l.leaseTTL / time.Second),
	})
	if err != nil {
		return err
	}

	tlsConfig, tlsCloser, err := keyless.BuildClientTLSConfig(l.client.baseURL.String(), resp.Hostnames)
	if err != nil {
		_ = client.unregisterLease(context.Background(), resp.LeaseID, l.reverseToken)
		return err
	}

	l.mu.Lock()
	if l.runID != runID || l.state == listenerStateClosed {
		l.mu.Unlock()
		_ = client.unregisterLease(context.Background(), resp.LeaseID, l.reverseToken)
		_ = tlsCloser.Close()
		return context.Canceled
	}
	oldCloser := l.tlsCloser
	l.client = client
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

func (l *Listener) runSessionPool(runCtx context.Context, runID uint64) {
	for {
		select {
		case <-runCtx.Done():
			return
		case <-l.refill:
		}

		for {
			l.mu.Lock()
			ready := l.runID == runID &&
				l.state == listenerStateReady &&
				l.client != nil &&
				strings.TrimSpace(l.leaseID) != "" &&
				l.tlsConfig != nil &&
				l.activeSessions < l.readyTarget
			if !ready {
				l.mu.Unlock()
				break
			}
			l.activeSessions++
			l.mu.Unlock()

			go l.runSession(runCtx, runID)
		}
	}
}

func (l *Listener) runRenewLoop(runCtx context.Context, runID uint64) {
	failures := 0
	wait := l.renewInterval

	for {
		if !sleepOrDone(runCtx, wait) {
			return
		}

		l.mu.Lock()
		current := l.runID == runID
		client := l.client
		leaseID := l.leaseID
		ready := l.state == listenerStateReady
		l.mu.Unlock()

		if !current || !ready || client == nil || strings.TrimSpace(leaseID) == "" {
			wait = l.renewInterval
			continue
		}

		ctx, cancel := context.WithTimeout(runCtx, 10*time.Second)
		err := client.renewLease(ctx, leaseID, l.reverseToken, l.leaseTTL)
		cancel()

		if err == nil {
			failures = 0
			wait = l.renewInterval
			continue
		}

		if isLeaseNotFound(err) {
			log.Warn().
				Str("component", "sdk-listener").
				Str("lease_id", leaseID).
				Msg("lease not found on relay, attempting re-registration")

			err = l.establish(runID, runCtx)
			if err == nil {
				failures = 0
				wait = l.renewInterval
				l.signalRefill()

				l.mu.Lock()
				leaseID = l.leaseID
				hostnames := append([]string(nil), l.hostnames...)
				l.mu.Unlock()

				log.Info().
					Str("component", "sdk-listener").
					Str("lease_id", leaseID).
					Strs("hostnames", hostnames).
					Msg("lease re-registered successfully")
				continue
			}
		}

		failures++
		event := log.Warn()
		if failures >= l.retryCount {
			event = log.Error()
		}
		event.Err(err).
			Str("component", "sdk-listener").
			Str("lease_id", leaseID).
			Int("consecutive_failures", failures).
			Msg("lease renewal failed")

		if failures >= l.retryCount {
			l.fail(runID, err, "listener renew retry limit reached")
			return
		}
		wait = l.retryDelay
	}
}

func (l *Listener) runSession(runCtx context.Context, runID uint64) {
	defer func() {
		l.mu.Lock()
		if l.runID == runID && l.activeSessions > 0 {
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
		if l.runID != runID {
			l.mu.Unlock()
			return
		}
		l.sessionFailures++
		failures := l.sessionFailures
		l.mu.Unlock()

		if failures >= l.retryCount {
			l.fail(runID, err, "listener session retry limit reached")
			return
		}

		_ = sleepOrDone(runCtx, l.retryDelay)
	}

	l.mu.Lock()
	ready := l.runID == runID && l.state == listenerStateReady
	client := l.client
	leaseID := l.leaseID
	tlsConfig := l.tlsConfig
	l.mu.Unlock()
	if !ready || client == nil || strings.TrimSpace(leaseID) == "" || tlsConfig == nil {
		return
	}

	conn, err := client.openReverseSession(runCtx, leaseID, l.reverseToken)
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
			l.sessionFailures = 0
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

func (l *Listener) closeCurrent() error {
	l.mu.Lock()
	l.state = listenerStateClosed
	cancel := l.cancel
	client := l.client
	leaseID := l.leaseID
	tlsCloser := l.tlsCloser
	l.leaseID = ""
	l.tlsConfig = nil
	l.tlsCloser = nil
	l.activeSessions = 0
	l.sessionFailures = 0
	l.runID++
	l.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	l.drainAccepted()

	var closeErr error
	if client != nil && strings.TrimSpace(leaseID) != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		closeErr = errors.Join(closeErr, client.unregisterLease(ctx, leaseID, l.reverseToken))
		cancel()
	}
	if tlsCloser != nil {
		closeErr = errors.Join(closeErr, tlsCloser.Close())
	}
	l.closeErr = closeErr
	return closeErr
}

func (l *Listener) fail(runID uint64, err error, message string) {
	closeErr, changed := l.markStale(runID)
	if !changed {
		return
	}
	if closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	log.Error().
		Str("component", "sdk-listener").
		Str("name", l.name).
		Err(err).
		Msg(message)
}

func (l *Listener) markStale(runID uint64) (error, bool) {
	l.mu.Lock()
	if l.runID != runID || l.state == listenerStateClosed || l.state == listenerStateStale {
		l.mu.Unlock()
		return nil, false
	}

	l.state = listenerStateStale
	cancel := l.cancel
	client := l.client
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
	if client != nil && strings.TrimSpace(leaseID) != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		closeErr = errors.Join(closeErr, client.unregisterLease(ctx, leaseID, l.reverseToken))
		cancel()
	}
	if tlsCloser != nil {
		closeErr = errors.Join(closeErr, tlsCloser.Close())
	}
	return closeErr, true
}

func (l *Listener) signalRefill() {
	select {
	case l.refill <- struct{}{}:
	default:
	}
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

func cloneMetadata(metadata types.LeaseMetadata) types.LeaseMetadata {
	return types.LeaseMetadata{
		Description: metadata.Description,
		Owner:       metadata.Owner,
		Thumbnail:   metadata.Thumbnail,
		Tags:        append([]string(nil), metadata.Tags...),
		Hide:        metadata.Hide,
	}
}

type listenerAddr string

func (a listenerAddr) Network() string { return "portal" }
func (a listenerAddr) String() string  { return string(a) }
