package sdk

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
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
	Transport        string // "tcp", "udp", or "both"; defaults to "tcp"
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

type listenerStatus string

const (
	listenerStatusInactive listenerStatus = "inactive"
	listenerStatusReady    listenerStatus = "ready"
)

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
	relayURL         string
	transport        string
	startupStatus    listenerStatus
	activeSessions   int
	leaseID          string
	hostname         string
	udpAddr          string
	quicAddr         string
	metadata         types.LeaseMetadata

	registered   chan struct{} // closed after first successful registration
	closeOnce    sync.Once
	registerOnce sync.Once
	mu           sync.Mutex
}

// NewListener creates one relay listener and its dedicated relay transport for one relay URL.
// Only local config validation fails immediately; relay startup runs in the background until ready.
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

	api, err := newApiClient(relayURL, cfg)
	if err != nil {
		cancel()
		return nil, err
	}

	transport := strings.ToLower(strings.TrimSpace(cfg.Transport))

	l := &Listener{
		doneCh:           listenerCtx.Done(),
		cancel:           cancel,
		api:              api,
		accepted:         make(chan net.Conn, max(readyTarget*2, 1)),
		registered:       make(chan struct{}),
		relayURL:         api.baseURL.String(),
		transport:        transport,
		startupStatus:    listenerStatusInactive,
		readyTarget:      readyTarget,
		retryCount:       cfg.RetryCount,
		retryWait:        retryWait,
		leaseTTL:         leaseTTL,
		renewBefore:      renewBefore,
		handshakeTimeout: handshakeTimeout,
	}

	go l.runStartup(listenerCtx)
	return l, nil
}

func (l *Listener) runStartup(ctx context.Context) {
	var retries int

	for {
		err := l.registerAndConfigure(ctx)
		switch {
		case err == nil:
			for i := 0; i < l.readyTarget; i++ {
				go l.runSessionLoop(ctx)
			}
			go l.runRenewLoop(ctx)
			publicURL := l.PublicURL()
			log.Info().
				Str("relay_url", l.relayURL).
				Str("lease_id", l.LeaseID()).
				Str("public_url", publicURL).
				Msg("service is available at this URL")
			return
		case errors.Is(err, context.Canceled), errors.Is(err, net.ErrClosed):
			return
		default:
			retries++
			if !l.retryOrClose(ctx, "lease registration", err, retries) {
				return
			}
		}
	}
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
	return l.metadata.Copy()
}

func (l *Listener) PublicURL() string {
	l.mu.Lock()
	hostname := l.hostname
	relayURL := l.relayURL
	l.mu.Unlock()

	if hostname == "" {
		return ""
	}

	parsed, err := url.Parse(relayURL)
	if err != nil || strings.TrimSpace(parsed.Scheme) == "" {
		return "https://" + hostname
	}

	host := hostname
	if port := strings.TrimSpace(parsed.Port()); port != "" {
		host = net.JoinHostPort(hostname, port)
	}

	return (&url.URL{
		Scheme: parsed.Scheme,
		Host:   host,
	}).String()
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
			if l.ActiveSessions() == 0 {
				l.setStartupStatus(listenerStatusInactive)
			}
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
		if !utils.SleepOrDone(ctx, interval) {
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
	l.sessionOpened()
	defer l.sessionClosed()

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

	if err := l.reregister(ctx); err != nil {
		return err
	}
	return nil
}

func (l *Listener) registerAndConfigure(ctx context.Context) error {
	if err := l.api.ensureReady(ctx); err != nil {
		return err
	}

	resp, err := l.api.registerLease(ctx, l.leaseTTL, l.transport)
	if err != nil {
		return err
	}

	tlsConf, tlsCloser, err := keyless.BuildClientTLSConfig(l.api.baseURL.String(), []string{resp.Hostname})
	if err != nil {
		_ = l.api.unregisterLease(context.Background(), resp.LeaseID)
		return err
	}

	if ctx.Err() != nil {
		_ = l.api.unregisterLease(context.Background(), resp.LeaseID)
		_ = tlsCloser.Close()
		return ctx.Err()
	}

	l.mu.Lock()
	if ctx.Err() != nil {
		l.mu.Unlock()
		_ = l.api.unregisterLease(context.Background(), resp.LeaseID)
		_ = tlsCloser.Close()
		return ctx.Err()
	}
	oldCloser := l.tlsCloser
	l.leaseID = resp.LeaseID
	l.hostname = resp.Hostname
	l.udpAddr = resp.UDPAddr
	l.quicAddr = resp.QUICAddr
	l.metadata = resp.Metadata.Copy()
	l.tlsConfig = tlsConf
	l.tlsCloser = tlsCloser
	l.mu.Unlock()

	if oldCloser != nil {
		_ = oldCloser.Close()
	}
	l.registerOnce.Do(func() { close(l.registered) })
	return nil
}

// WaitRegistered blocks until the first successful lease registration or context cancellation.
func (l *Listener) WaitRegistered(ctx context.Context) error {
	select {
	case <-l.registered:
		return nil
	case <-l.doneCh:
		return net.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Listener) reregister(ctx context.Context) error {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	return l.registerAndConfigure(requestCtx)
}

func isLeaseNotFound(err error) bool {
	return errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeLeaseNotFound})
}

func (l *Listener) retryOrClose(ctx context.Context, operation string, err error, retries int) bool {
	if ctx.Err() != nil {
		return false
	}

	logger := log.With().
		Str("relay_url", l.relayURL).
		Str("operation", operation).
		Str("lease_id", l.LeaseID()).
		Logger()

	if operation == "lease registration" {
		l.setStartupStatus(listenerStatusInactive)
	}

	if l.retryCount > 0 && retries > l.retryCount {
		if operation != "lease renewal" {
			logger.Error().
				Err(err).
				Int("retry_count", l.retryCount).
				Msg("retry budget exhausted; closing listener")
		}
		_ = l.Close()
		return false
	}

	if operation != "lease renewal" {
		logger.Debug().
			Err(err).
			Int("retry_attempt", retries).
			Int("retry_count", l.retryCount).
			Dur("retry_wait", l.retryWait).
			Msg("operation failed; retrying")
	}

	return utils.SleepOrDone(ctx, l.retryWait)
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

func (l *Listener) setStartupStatus(status listenerStatus) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.startupStatus = status
	l.mu.Unlock()
}

func (l *Listener) StartupStatus() listenerStatus {
	if l == nil {
		return listenerStatusInactive
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	return l.startupStatus
}

func (l *Listener) ActiveSessions() int {
	if l == nil {
		return 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	return l.activeSessions
}

func (l *Listener) sessionOpened() {
	if l == nil {
		return
	}

	l.mu.Lock()
	l.activeSessions++
	l.startupStatus = listenerStatusReady
	l.mu.Unlock()
}

func (l *Listener) sessionClosed() {
	if l == nil {
		return
	}

	l.mu.Lock()
	if l.activeSessions > 0 {
		l.activeSessions--
	}
	l.mu.Unlock()
}
