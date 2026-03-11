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
	"github.com/gosuda/portal/v2/utils"
)

type ListenerConfig struct {
	Name             string
	ReverseToken     string
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
	doneCh           <-chan struct{}
	cancel           context.CancelFunc
	api              *apiClient
	accepted         chan net.Conn
	leaseID          string
	hostname         string
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
	readyTarget := utils.IntOrDefault(cfg.ReadyTarget, defaultReadyTarget)
	leaseTTL := utils.DurationOrDefault(cfg.LeaseTTL, defaultLeaseTTL)
	handshakeTimeout := utils.DurationOrDefault(cfg.HandshakeTimeout, defaultHandshakeTimeout)
	renewBefore := utils.DurationOrDefault(cfg.RenewBefore, defaultRenewBefore)
	retryWait := utils.DurationOrDefault(cfg.RetryWait, defaultRetryWait)

	api, err := newApiClient(listenerCtx, relayURL, cfg)
	if err != nil {
		cancel()
		return nil, err
	}

	l := &Listener{
		doneCh:           listenerCtx.Done(),
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

	resp, err := api.registerLease(listenerCtx, leaseTTL)
	if err != nil {
		api.close()
		cancel()
		return nil, err
	}

	tlsConf, tlsCloser, err := keyless.BuildClientTLSConfig(api.baseURL.String(), []string{resp.Hostname})
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
	l.hostname = resp.Hostname
	l.metadata = cloneMetadata(resp.Metadata)
	l.tlsConfig = tlsConf
	l.tlsCloser = tlsCloser
	l.mu.Unlock()

	for i := 0; i < l.readyTarget; i++ {
		go l.runSessionLoop(listenerCtx)
	}
	go l.runRenewLoop(listenerCtx)
	return l, nil
}

func (l *Listener) Accept() (net.Conn, error) {
	select {
	case <-l.doneCh:
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
		l.hostname = ""
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

func (l *Listener) Hostname() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hostname
}

func (l *Listener) Metadata() types.LeaseMetadata {
	l.mu.Lock()
	defer l.mu.Unlock()
	return cloneMetadata(l.metadata)
}

func (l *Listener) PublicURL() string {
	l.mu.Lock()
	hostname := l.hostname
	l.mu.Unlock()

	if hostname == "" {
		return ""
	}
	return "https://" + hostname
}

func (l *Listener) runSessionLoop(ctx context.Context) {
	var retries int

	for {
		claimed, err := l.runSession(ctx)
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
			if !l.retryOrClose(ctx, "reverse session connect", err, retries) {
				return
			}
		}
	}
}

func (l *Listener) runRenewLoop(ctx context.Context) {
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
		if !sleepOrDone(ctx, interval) {
			return
		}

		var retries int
		for {
			err := l.renewLease(ctx)
			if err == nil {
				break
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
				return
			}

			retries++
			if !l.retryOrClose(ctx, "lease renewal", err, retries) {
				return
			}
		}
	}
}

func (l *Listener) runSession(ctx context.Context) (bool, error) {
	l.mu.Lock()
	leaseID := l.leaseID
	l.mu.Unlock()

	conn, err := l.api.openReverseSession(ctx, leaseID)
	if err != nil {
		return false, err
	}

	var marker [1]byte
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * l.handshakeTimeout))
		if _, err := io.ReadFull(conn, marker[:]); err != nil {
			_ = conn.Close()
			return false, err
		}
		_ = conn.SetReadDeadline(time.Time{})

		switch marker[0] {
		case types.MarkerKeepalive:
			continue
		case types.MarkerTLSStart:
			if err := l.activate(ctx, conn); err != nil {
				_ = conn.Close()
				return true, err
			}
			return true, nil
		default:
			_ = conn.Close()
			return false, fmt.Errorf("unexpected reverse marker: 0x%02x", marker[0])
		}
	}
}

func (l *Listener) activate(ctx context.Context, conn net.Conn) error {
	l.mu.Lock()
	tlsCfg := l.tlsConfig
	l.mu.Unlock()

	tlsConn := tls.Server(conn, tlsCfg)
	handshakeCtx, cancel := context.WithTimeout(ctx, l.handshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		_ = tlsConn.Close()
		return ctx.Err()
	case l.accepted <- tlsConn:
		return nil
	}
}

func (l *Listener) renewLease(ctx context.Context) error {
	l.mu.Lock()
	leaseID := l.leaseID
	l.mu.Unlock()

	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := l.api.renewLease(requestCtx, leaseID, l.leaseTTL)
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

	if err := l.reregister(ctx); err != nil {
		return err
	}

	log.Info().
		Str("component", "sdk-listener").
		Str("lease_id", l.LeaseID()).
		Str("hostname", l.Hostname()).
		Msg("lease re-registered successfully")
	return nil
}

func (l *Listener) reregister(ctx context.Context) error {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	resp, err := l.api.registerLease(requestCtx, l.leaseTTL)
	if err != nil {
		return err
	}

	tlsConf, tlsCloser, err := keyless.BuildClientTLSConfig(l.api.baseURL.String(), []string{resp.Hostname})
	if err != nil {
		_ = l.api.unregisterLease(requestCtx, resp.LeaseID)
		return err
	}

	if ctx.Err() != nil {
		_ = l.api.unregisterLease(context.Background(), resp.LeaseID)
		_ = tlsCloser.Close()
		return ctx.Err()
	}

	l.mu.Lock()
	oldCloser := l.tlsCloser
	l.leaseID = resp.LeaseID
	l.hostname = resp.Hostname
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

func (l *Listener) retryOrClose(ctx context.Context, operation string, err error, retries int) bool {
	if ctx.Err() != nil {
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

	return sleepOrDone(ctx, l.retryWait)
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

type listenerAddr string

func (a listenerAddr) Network() string { return "portal" }
func (a listenerAddr) String() string  { return string(a) }

func (l *Listener) done() bool {
	select {
	case <-l.doneCh:
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
