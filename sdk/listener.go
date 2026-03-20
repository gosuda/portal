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
	OwnerAddress     string
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

	RegisterBootstraps []string
}

type listenerStatus string

const (
	listenerStatusInactive listenerStatus = "inactive"
	listenerStatusReady    listenerStatus = "ready"
)

type Listener struct {
	api    *apiClient
	cancel context.CancelFunc
	doneCh <-chan struct{}

	retryCount  int
	retryWait   time.Duration
	leaseTTL    time.Duration
	renewBefore time.Duration

	stream   *transport.ClientStream
	datagram *transport.ClientDatagram

	registered   chan struct{}
	closeOnce    sync.Once
	registerOnce sync.Once

	mu            sync.Mutex
	startupStatus listenerStatus
	leaseID       string
	hostname      string
	udpAddr       string
	metadata      types.LeaseMetadata
	tlsConfig     *tls.Config
	tlsCloser     io.Closer
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

	initialBootstraps, err := utils.NormalizeRelayURLs(cfg.RegisterBootstraps)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("normalize bootstraps: %w", err)
	}

	l := &Listener{
		doneCh:        listenerCtx.Done(),
		cancel:        cancel,
		api:           api,
		registered:    make(chan struct{}),
		startupStatus: listenerStatusInactive,
		retryCount:    cfg.RetryCount,
		retryWait:     retryWait,
		leaseTTL:      leaseTTL,
		renewBefore:   renewBefore,
		metadata:      cfg.Metadata.Copy(),
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

	go l.runStartup(listenerCtx, initialBootstraps, readyTarget)
	return l, nil
}

func (l *Listener) runStartup(ctx context.Context, initialBootstraps []string, readyTarget int) {
	var retries int

	for {
		err := l.registerAndConfigure(ctx, initialBootstraps)
		switch {
		case err == nil:
			for range readyTarget {
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
				Str("relay_url", l.api.baseURL.String()).
				Str("lease_id", l.LeaseID())
			if publicURL != "" {
				event = event.Str("public_url", publicURL)
			}
			event.Msg("relay listener registered")
			return
		case errors.Is(err, context.Canceled), errors.Is(err, net.ErrClosed):
			return
		default:
			if errors.Is(err, errRelayIncompatible) ||
				errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeFeatureUnavailable}) ||
				errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeTransportMismatch}) ||
				errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeHostnameConflict}) ||
				errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeIPBanned}) {
				log.Error().
					Err(err).
					Str("relay_url", l.api.baseURL.String()).
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
	if l == nil || l.api == nil || l.api.baseURL == nil {
		return ""
	}

	l.mu.Lock()
	hostname := l.hostname
	l.mu.Unlock()

	if hostname == "" {
		return ""
	}

	if strings.TrimSpace(l.api.baseURL.Scheme) == "" {
		return "https://" + hostname
	}

	host := hostname
	if port := strings.TrimSpace(l.api.baseURL.Port()); port != "" {
		host = net.JoinHostPort(hostname, port)
	}

	return (&url.URL{
		Scheme: l.api.baseURL.Scheme,
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
	if l == nil || l.datagram == nil {
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
	if l == nil || l.datagram == nil {
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

	requestCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := l.registerAndConfigure(requestCtx, nil); err != nil {
		return err
	}
	return nil
}

func (l *Listener) registerAndConfigure(ctx context.Context, registerBootstraps []string) error {
	if err := l.api.ensureReady(ctx); err != nil {
		return err
	}

	resp, err := l.api.registerLease(ctx, l.leaseTTL, l.datagram != nil, registerBootstraps)
	if err != nil {
		return err
	}
	if l.datagram != nil && !resp.UDPEnabled {
		_ = l.api.unregisterLease(context.Background(), resp.LeaseID)
		return &types.APIRequestError{
			Code:    types.APIErrorCodeFeatureUnavailable,
			Message: "relay did not enable required udp support",
		}
	}

	tlsConf, tlsCloser, err := keyless.BuildClientTLSConfig(l.api.baseURL.String(), []string{resp.Hostname})
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
	return l != nil && l.datagram != nil
}

func (l *Listener) SupportsStream() bool {
	return l != nil
}

func (l *Listener) UDPEnabled() bool {
	return l != nil && l.datagram != nil
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

func (l *Listener) retryOrClose(ctx context.Context, operation string, err error, retries int) bool {
	if ctx.Err() != nil {
		return false
	}

	logger := log.With().
		Str("relay_url", l.api.baseURL.String()).
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

func (l *Listener) closed() bool {
	if l == nil || l.doneCh == nil {
		return true
	}
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
