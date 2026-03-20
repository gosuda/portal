package sdk

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/portal/transport"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

type ListenerConfig struct {
	Name             string
	ReverseToken     string
	UDPEnabled       bool
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
	tlsCloser     io.Closer
	tlsConfig     *tls.Config
	readyTarget   int
	retryCount    int
	retryWait     time.Duration
	leaseTTL      time.Duration
	renewBefore   time.Duration
	doneCh        <-chan struct{}
	cancel        context.CancelFunc
	api           *apiClient
	relayURL      string
	startupStatus listenerStatus
	leaseID       string
	hostname      string
	udpAddr       string
	udpEnabled    bool
	metadata      types.LeaseMetadata
	stream        *transport.ClientStream
	datagram      *transport.ClientDatagram

	registered   chan struct{} // closed after first successful registration
	closeOnce    sync.Once
	registerOnce sync.Once
	mu           sync.Mutex
}

// NewListener creates one relay listener and its dedicated relay transport for one relay URL.
// Only local config validation fails immediately; relay startup runs in the background until ready.
func NewListener(ctx context.Context, relayURL string, cfg ListenerConfig) (*Listener, error) {
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

	l := &Listener{
		doneCh:        listenerCtx.Done(),
		cancel:        cancel,
		api:           api,
		registered:    make(chan struct{}),
		relayURL:      api.baseURL.String(),
		startupStatus: listenerStatusInactive,
		readyTarget:   readyTarget,
		retryCount:    cfg.RetryCount,
		retryWait:     retryWait,
		leaseTTL:      leaseTTL,
		renewBefore:   renewBefore,
		udpEnabled:    cfg.UDPEnabled,
	}
	l.stream = transport.NewClientStream(readyTarget, handshakeTimeout)
	if cfg.UDPEnabled {
		l.datagram = transport.NewClientDatagram(func(err error) {
			log.Warn().
				Err(err).
				Str("component", "sdk-datagram-plane").
				Str("lease_id", l.LeaseID()).
				Msg("quic receive loop ended")
		})
	}

	if l.datagram != nil {
		go l.datagram.RunLoop(listenerCtx, l.currentDatagramState, func(ctx context.Context, state transport.ClientDatagramState) (*quic.Conn, error) {
			return l.api.openQUICSession(ctx, state.LeaseID, state.ReverseToken)
		})
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
				go l.stream.RunLoop(
					ctx,
					func(ctx context.Context) (net.Conn, error) {
						l.mu.Lock()
						leaseID := l.leaseID
						l.mu.Unlock()
						return l.api.openReverseSession(ctx, leaseID)
					},
					func() *tls.Config {
						l.mu.Lock()
						defer l.mu.Unlock()
						return l.tlsConfig
					},
					func() { l.setStartupStatus(listenerStatusReady) },
					func() { l.setStartupStatus(listenerStatusInactive) },
					l.retryOrClose,
				)
			}
			go l.runRenewLoop(ctx)
			publicURL := l.PublicURL()
			event := log.Info().
				Str("relay_url", l.relayURL).
				Str("lease_id", l.LeaseID())
			if publicURL != "" {
				event = event.Str("public_url", publicURL)
			}
			event.Msg("relay listener registered")
			return
		case errors.Is(err, context.Canceled), errors.Is(err, net.ErrClosed):
			return
		default:
			if isPermanentRegistrationError(err) {
				log.Error().
					Err(err).
					Str("relay_url", l.relayURL).
					Str("lease_id", l.LeaseID()).
					Msg("lease registration failed; closing listener")
				_ = l.Close()
				return
			}
			retries++
			if !l.retryOrClose(ctx, "lease registration", err, retries) {
				return
			}
		}
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
		stream := l.stream
		datagram := l.datagram
		api := l.api
		l.leaseID = ""
		l.hostname = ""
		l.udpAddr = ""
		l.tlsConfig = nil
		l.tlsCloser = nil
		l.mu.Unlock()

		if stream != nil {
			stream.Drain()
		}
		if datagram != nil {
			datagram.Close()
		}

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

func (l *Listener) Accept() (net.Conn, error) {
	if l.stream == nil {
		return nil, net.ErrClosed
	}
	return l.stream.Accept(l.doneCh)
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

func (l *Listener) ActiveSessions() int {
	if l == nil || l.stream == nil {
		return 0
	}
	return l.stream.ActiveSessions()
}

func (l *Listener) AcceptDatagram() (types.DatagramFrame, error) {
	if l == nil || !l.activeSupportsDatagram() || l.datagram == nil {
		return types.DatagramFrame{}, net.ErrClosed
	}
	return l.datagram.Accept(l.doneCh)
}

func (l *Listener) SendDatagram(flowID uint32, payload []byte) error {
	if l == nil || !l.activeSupportsDatagram() || l.datagram == nil {
		return net.ErrClosed
	}
	return l.datagram.Send(flowID, payload)
}

func (l *Listener) UDPAddr() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.udpAddr
}

func (l *Listener) currentDatagramState() (transport.ClientDatagramState, bool) {
	if l == nil || !l.activeSupportsDatagram() {
		return transport.ClientDatagramState{}, false
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.api == nil || l.leaseID == "" {
		return transport.ClientDatagramState{}, false
	}

	return transport.ClientDatagramState{
		LeaseID:      l.leaseID,
		ReverseToken: l.api.reverseToken,
	}, true
}

func (l *Listener) WaitDatagramReady(ctx context.Context) error {
	if l == nil || !l.UDPEnabled() {
		return errors.New("lease does not have udp enabled")
	}
	if err := l.WaitRegistered(ctx); err != nil {
		return err
	}
	if !l.activeSupportsDatagram() {
		return errors.New("relay did not enable udp")
	}
	if l.UDPAddr() == "" {
		return errors.New("lease registration did not expose udp address")
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if l.datagramConnected() {
			return nil
		}

		select {
		case <-l.doneCh:
			return net.ErrClosed
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l *Listener) activeSupportsDatagram() bool {
	if l == nil || !l.udpEnabled {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.udpAddr != ""
}

func (l *Listener) datagramConnected() bool {
	return l != nil && l.datagram != nil && l.datagram.Connected()
}

func (l *Listener) datagramNegotiationState() (registered bool, enabled bool) {
	if l == nil {
		return true, false
	}
	select {
	case <-l.registered:
		return true, l.activeSupportsDatagram()
	default:
		return false, false
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
	if !errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeLeaseNotFound}) {
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

	resp, err := l.api.registerLease(ctx, l.leaseTTL, l.udpEnabled)
	if err != nil {
		return err
	}
	if l.udpEnabled && !resp.UDPEnabled {
		_ = l.api.unregisterLease(context.Background(), resp.LeaseID)
		return &types.APIRequestError{
			Code:    types.APIErrorCodeFeatureUnavailable,
			Message: "relay did not enable required udp support",
		}
	}

	var (
		tlsConf   *tls.Config
		tlsCloser io.Closer
	)
	tlsConf, tlsCloser, err = keyless.BuildClientTLSConfig(l.api.baseURL.String(), []string{resp.Hostname})
	if err != nil {
		_ = l.api.unregisterLease(context.Background(), resp.LeaseID)
		return err
	}

	if ctx.Err() != nil {
		_ = l.api.unregisterLease(context.Background(), resp.LeaseID)
		if tlsCloser != nil {
			_ = tlsCloser.Close()
		}
		return ctx.Err()
	}

	l.mu.Lock()
	if ctx.Err() != nil {
		l.mu.Unlock()
		_ = l.api.unregisterLease(context.Background(), resp.LeaseID)
		if tlsCloser != nil {
			_ = tlsCloser.Close()
		}
		return ctx.Err()
	}
	oldCloser := l.tlsCloser
	datagram := l.datagram
	l.leaseID = resp.LeaseID
	l.hostname = resp.Hostname
	l.udpAddr = resp.UDPAddr
	l.metadata = resp.Metadata.Copy()
	l.tlsConfig = tlsConf
	l.tlsCloser = tlsCloser
	l.mu.Unlock()

	if oldCloser != nil {
		_ = oldCloser.Close()
	}
	if datagram != nil {
		datagram.Clear("lease updated")
	}
	l.registerOnce.Do(func() { close(l.registered) })
	return nil
}

func (l *Listener) SupportsDatagram() bool {
	return l != nil && l.udpEnabled
}

func (l *Listener) SupportsStream() bool {
	return l != nil
}

func (l *Listener) UDPEnabled() bool {
	return l != nil && l.udpEnabled
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

func isPermanentRegistrationError(err error) bool {
	return errors.Is(err, errRelayIncompatible) ||
		errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeFeatureUnavailable}) ||
		errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeTransportMismatch}) ||
		errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeHostnameConflict}) ||
		errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeIPBanned})
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
