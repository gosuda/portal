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
	RetryCount       int
	RetryWait        time.Duration
}

type Listener struct {
	tlsCloser        io.Closer
	tlsConfig        *tls.Config
	readyTarget      int
	retryCount       int
	retryWait        time.Duration
	leaseTTL         time.Duration
	renewBefore      time.Duration
	handshakeTimeout time.Duration
	ctx              context.Context
	cancel           context.CancelFunc
	api              *apiClient
	accepted         chan net.Conn
	leaseID          string
	hostnames        []string
	metadata         types.LeaseMetadata

	closeOnce sync.Once
	mu        sync.Mutex
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
	retryWait := cfg.RetryWait
	if retryWait <= 0 {
		retryWait = defaultRetryWait
	}

	api, err := newApiClient(listenerCtx, relayURL, cfg)
	if err != nil {
		cancel()
		return nil, err
	}

	l := &Listener{
		ctx:              listenerCtx,
		cancel:           cancel,
		api:              api,
		accepted:         make(chan net.Conn, max(readyTarget*2, 1)),
		readyTarget:      readyTarget,
		retryCount:       cfg.RetryCount,
		retryWait:        retryWait,
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

	for i := 0; i < l.readyTarget; i++ {
		go l.runSessionLoop()
	}
	go l.runRenewLoop()
	return l, nil
}

func (l *Listener) Accept() (net.Conn, error) {
	select {
	case <-l.ctx.Done():
		return nil, net.ErrClosed
	case conn := <-l.accepted:
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
		return listenerAddr("portal:closed")
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

func (l *Listener) runSessionLoop() {
	var retries int

	for {
		claimed, err := l.runSession()
		switch {
		case err == nil:
			retries = 0
		case errors.Is(err, context.Canceled), errors.Is(err, net.ErrClosed):
			return
		case claimed:
			// A claimed connection already reached the data plane.
			// Do not spend retry budget on browser-side TLS failures or disconnects.
			retries = 0
		default:
			retries++
			if !l.retryOrClose("reverse session connect", err, retries) {
				return
			}
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

	for {
		sleepOrDone(l.context(), interval)
		if l.isClosed() {
			return
		}

		var retries int
		for {
			err := l.renewLease()
			switch {
			case err == nil:
				goto nextRenew
			case errors.Is(err, context.Canceled), errors.Is(err, net.ErrClosed):
				return
			default:
				retries++
				if !l.retryOrClose("lease renewal", err, retries) {
					return
				}
			}
		}

	nextRenew:
	}
}

func (l *Listener) runSession() (bool, error) {
	sessionCtx := l.context()
	l.mu.Lock()
	leaseID := l.leaseID
	l.mu.Unlock()

	conn, err := l.api.openReverseSession(sessionCtx, leaseID)
	if err != nil {
		return false, err
	}

	claimed, err := l.awaitActivation(conn)
	if err != nil {
		_ = conn.Close()
		return claimed, err
	}
	return claimed, nil
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

func (l *Listener) renewLease() error {
	l.mu.Lock()
	leaseID := l.leaseID
	l.mu.Unlock()

	ctx, cancel := context.WithTimeout(l.context(), 10*time.Second)
	err := l.api.renewLease(ctx, leaseID, l.leaseTTL)
	cancel()
	if err == nil {
		return nil
	}
	if !isLeaseNotFound(err) {
		return err
	}

	log.Warn().
		Err(err).
		Str("component", "sdk-listener").
		Str("lease_id", leaseID).
		Msg("lease not found on relay, attempting re-registration")

	if err := l.reregister(); err != nil {
		return err
	}

	log.Info().
		Str("component", "sdk-listener").
		Str("lease_id", l.LeaseID()).
		Strs("hostnames", l.Hostnames()).
		Msg("lease re-registered successfully")
	return nil
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
	return nil
}

func isLeaseNotFound(err error) bool {
	return errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeLeaseNotFound})
}

func (l *Listener) retryOrClose(operation string, err error, retries int) bool {
	if l.isClosed() {
		return false
	}

	logger := log.With().
		Str("component", "sdk-listener").
		Str("operation", operation).
		Str("lease_id", l.LeaseID()).
		Logger()

	if l.retryCount > 0 && retries > l.retryCount {
		logger.Error().
			Err(err).
			Int("retry_count", l.retryCount).
			Msg("retry budget exhausted; closing listener")
		_ = l.Close()
		return false
	}

	logger.Warn().
		Err(err).
		Int("retry_attempt", retries).
		Int("retry_count", l.retryCount).
		Dur("retry_wait", l.retryWait).
		Msg("operation failed; retrying")

	sleepOrDone(l.context(), l.retryWait)
	return !l.isClosed()
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
