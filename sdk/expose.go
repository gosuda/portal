package sdk

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

// Exposure owns the lifecycle of one or more relay listeners and accepts
// traffic from all of them through one net.Listener.
type Exposure struct {
	listener net.Listener
	relays   []exposureRelay

	closeOnce sync.Once
	connSeq   atomic.Uint64
}

// Expose creates relay listeners for each normalized relay URL and exposes a
// merged listener for accepting traffic from all of them. Empty relay input
// returns nil, nil so callers can fall back to local-only serving.
func Expose(ctx context.Context, relayUrls []string, name string, metadata types.LeaseMetadata) (*Exposure, error) {
	relayURLs, err := utils.NormalizeRelayURLs(relayUrls)
	if err != nil {
		return nil, err
	}
	if len(relayURLs) == 0 {
		return nil, nil
	}

	relays := make([]exposureRelay, 0, len(relayURLs))
	cleanup := func() error {
		var closeErr error
		for _, relay := range relays {
			if relay.listener != nil {
				closeErr = errors.Join(closeErr, relay.listener.Close())
			}
		}
		return closeErr
	}

	for _, relayURL := range relayURLs {
		listener, err := NewListener(ctx, relayURL, ListenerConfig{
			Name:     name,
			Metadata: metadata,
		})
		if err != nil {
			return nil, errors.Join(fmt.Errorf("listen %q: %w", relayURL, err), cleanup())
		}

		relays = append(relays, exposureRelay{
			relayURL: relayURL,
			listener: listener,
		})
	}

	listeners := make([]net.Listener, 0, len(relays))
	for _, relay := range relays {
		listeners = append(listeners, relay.listener)
	}

	merged, err := mergeListeners(listeners...)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("merge listeners: %w", err), cleanup())
	}

	exposure := &Exposure{
		listener: merged,
		relays:   relays,
	}

	logger := log.With().Str("component", "sdk-exposure").Logger()
	logger.Info().
		Int("relay_count", len(exposure.relays)).
		Strs("relays", exposure.RelayURLs()).
		Strs("public_urls", exposure.PublicURLs()).
		Msg("exposure ready")

	return exposure, nil
}

// Accept implements net.Listener by accepting from the merged relay listener.
func (e *Exposure) Accept() (net.Conn, error) {
	if e == nil || e.listener == nil {
		return nil, net.ErrClosed
	}

	conn, err := e.listener.Accept()
	if err != nil {
		if !errors.Is(err, net.ErrClosed) {
			logger := log.With().Str("component", "sdk-exposure").Logger()
			logger.Warn().
				Err(err).
				Str("local_addr", exposureAddrString(e.listener.Addr())).
				Msg("exposure accept failed")
		}
		return nil, err
	}

	connID := e.connSeq.Add(1)
	logger := log.With().Str("component", "sdk-exposure").Logger()
	logger.Info().
		Uint64("conn_id", connID).
		Str("local_addr", exposureAddrString(conn.LocalAddr())).
		Str("remote_addr", exposureAddrString(conn.RemoteAddr())).
		Msg("exposure connection accepted")

	return &exposureConn{
		Conn:       conn,
		id:         connID,
		localAddr:  exposureAddrString(conn.LocalAddr()),
		remoteAddr: exposureAddrString(conn.RemoteAddr()),
	}, nil
}

// Addr implements net.Listener.
func (e *Exposure) Addr() net.Addr {
	if e == nil || e.listener == nil {
		return listenerAddr("portal:exposure")
	}
	return e.listener.Addr()
}

// RelayURLs returns the normalized relay URLs backing the exposure.
func (e *Exposure) RelayURLs() []string {
	if e == nil || len(e.relays) == 0 {
		return nil
	}

	out := make([]string, 0, len(e.relays))
	for _, relay := range e.relays {
		out = append(out, relay.relayURL)
	}
	return out
}

// PublicURLs returns the de-duplicated public URLs exposed by the exposure.
func (e *Exposure) PublicURLs() []string {
	if e == nil || len(e.relays) == 0 {
		return nil
	}

	out := make([]string, 0, len(e.relays))
	seen := make(map[string]struct{})
	for _, relay := range e.relays {
		if relay.listener == nil {
			continue
		}
		for _, rawURL := range relay.listener.PublicURLs() {
			if _, ok := seen[rawURL]; ok {
				continue
			}
			seen[rawURL] = struct{}{}
			out = append(out, rawURL)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RunHTTP serves one handler on the exposure and, when localAddr is set, on
// the provided local HTTP address for app-local access. Nil exposures support
// local-only serving.
func (e *Exposure) RunHTTP(ctx context.Context, handler http.Handler, localAddr string) error {
	var relayListener net.Listener
	if e != nil {
		relayListener = e
	}
	return RunHTTP(ctx, relayListener, handler, localAddr)
}

// Close closes the merged listener and all underlying relay listeners.
func (e *Exposure) Close() error {
	if e == nil {
		return nil
	}

	var closeErr error
	e.closeOnce.Do(func() {
		if e.listener != nil {
			closeErr = errors.Join(closeErr, e.listener.Close())
		}

		logger := log.With().Str("component", "sdk-exposure").Logger()
		event := logger.Info().
			Int("relay_count", len(e.relays)).
			Strs("relays", e.RelayURLs())
		if closeErr != nil {
			event = logger.Warn().
				Err(closeErr).
				Int("relay_count", len(e.relays)).
				Strs("relays", e.RelayURLs())
		}
		event.Msg("exposure closed")
	})
	return closeErr
}

// RunHTTP serves one handler on relayListener and, when localAddr is set, on
// the provided local HTTP address for app-local access.
func RunHTTP(ctx context.Context, relayListener net.Listener, handler http.Handler, localAddr string) error {
	localAddr = strings.TrimSpace(localAddr)
	if ctx == nil {
		ctx = context.Background()
	}

	if relayListener == nil && localAddr == "" {
		return errors.New("relay listener or local address is required")
	}

	var relaySrv *http.Server
	if relayListener != nil {
		relaySrv = &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: defaultRequestTimeout,
		}
	}

	var localSrv *http.Server
	if localAddr != "" {
		localSrv = &http.Server{
			Addr:              localAddr,
			Handler:           handler,
			ReadHeaderTimeout: defaultRequestTimeout,
		}
	}

	serverCount := 0
	if relaySrv != nil {
		serverCount++
	}
	if localSrv != nil {
		serverCount++
	}

	results := make(chan error, serverCount)
	normalizeServeErr := func(err error, prefix string) error {
		if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return fmt.Errorf("%s: %w", prefix, err)
	}

	var (
		shutdownOnce sync.Once
		shutdownErr  error
	)
	shutdown := func() error {
		shutdownOnce.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultHTTPShutdownTimeout)
			defer cancel()

			var localErr error
			if localSrv != nil {
				localErr = localSrv.Shutdown(shutdownCtx)
				if errors.Is(localErr, http.ErrServerClosed) {
					localErr = nil
				}
			}

			var relayErr error
			if relaySrv != nil {
				relayErr = relaySrv.Shutdown(shutdownCtx)
				if errors.Is(relayErr, http.ErrServerClosed) {
					relayErr = nil
				}
			}

			shutdownErr = errors.Join(localErr, relayErr)
		})
		return shutdownErr
	}

	if localSrv != nil {
		go func() {
			results <- normalizeServeErr(localSrv.ListenAndServe(), "serve local http")
		}()
	}
	if relaySrv != nil {
		go func() {
			results <- normalizeServeErr(relaySrv.Serve(relayListener), "serve relay http")
		}()
	}

	var serveErr error
	remaining := serverCount
	ctxDone := ctx.Done()
	for remaining > 0 {
		select {
		case err := <-results:
			remaining--
			if err != nil {
				serveErr = errors.Join(serveErr, err)
				_ = shutdown()
			}
		case <-ctxDone:
			_ = shutdown()
			ctxDone = nil
		}
	}

	return errors.Join(serveErr, shutdownErr)
}

type exposureRelay struct {
	relayURL string
	listener *Listener
}

type exposureConn struct {
	net.Conn
	id         uint64
	localAddr  string
	remoteAddr string
	closeOnce  sync.Once
}

func (c *exposureConn) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		closeErr = c.Conn.Close()
		if errors.Is(closeErr, net.ErrClosed) {
			closeErr = nil
		}

		logger := log.With().Str("component", "sdk-exposure").Logger()
		event := logger.Info().
			Uint64("conn_id", c.id).
			Str("local_addr", c.localAddr).
			Str("remote_addr", c.remoteAddr)
		if closeErr != nil {
			event = logger.Warn().
				Err(closeErr).
				Uint64("conn_id", c.id).
				Str("local_addr", c.localAddr).
				Str("remote_addr", c.remoteAddr)
		}
		event.Msg("exposure connection closed")
	})
	return closeErr
}

func exposureAddrString(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

// mergeListeners fans in multiple listeners into one net.Listener. It keeps
// serving accepts from remaining listeners when one listener stops, and returns
// a terminal error only after all source listeners have stopped.
func mergeListeners(listeners ...net.Listener) (net.Listener, error) {
	if len(listeners) == 0 {
		return nil, errors.New("at least one listener is required")
	}

	merged := &mergedListener{
		listeners: make([]net.Listener, 0, len(listeners)),
		accepted:  make(chan net.Conn),
		closed:    make(chan struct{}),
	}
	for i, listener := range listeners {
		if listener == nil {
			return nil, fmt.Errorf("listener %d is nil", i)
		}
		merged.listeners = append(merged.listeners, listener)
	}

	merged.addr = merged.buildAddr()
	merged.active = len(merged.listeners)
	for _, listener := range merged.listeners {
		source := listener
		go merged.runAcceptLoop(source)
	}
	return merged, nil
}

type mergedListener struct {
	listeners []net.Listener
	accepted  chan net.Conn
	closed    chan struct{}
	addr      net.Addr

	closeOnce   sync.Once
	mu          sync.Mutex
	active      int
	terminalErr error
}

func (l *mergedListener) Accept() (net.Conn, error) {
	conn, ok := <-l.accepted
	if ok {
		select {
		case <-l.closed:
			_ = conn.Close()
			return nil, l.terminalErrorOr(net.ErrClosed)
		default:
		}
		return conn, nil
	}

	return nil, l.terminalErrorOr(net.ErrClosed)
}

func (l *mergedListener) Close() error {
	var closeErr error
	l.closeOnce.Do(func() {
		close(l.closed)
		for _, listener := range l.listeners {
			err := listener.Close()
			if errors.Is(err, net.ErrClosed) {
				err = nil
			}
			closeErr = errors.Join(closeErr, err)
		}
		l.recordTerminalError(closeErr)
	})
	return closeErr
}

func (l *mergedListener) Addr() net.Addr {
	return l.addr
}

func (l *mergedListener) buildAddr() net.Addr {
	if len(l.listeners) == 1 {
		return l.listeners[0].Addr()
	}

	parts := make([]string, 0, len(l.listeners))
	for _, listener := range l.listeners {
		parts = append(parts, listener.Addr().String())
	}
	return listenerAddr("merged:" + strings.Join(parts, ","))
}

func (l *mergedListener) runAcceptLoop(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				l.recordTerminalError(fmt.Errorf("accept %s: %w", listener.Addr().String(), err))
			}
			l.finishWorker()
			return
		}

		select {
		case <-l.closed:
			_ = conn.Close()
			l.finishWorker()
			return
		default:
		}

		select {
		case l.accepted <- conn:
		case <-l.closed:
			_ = conn.Close()
			l.finishWorker()
			return
		}
	}
}

func (l *mergedListener) finishWorker() {
	l.mu.Lock()
	l.active--
	last := l.active == 0
	if last && l.terminalErr == nil {
		l.terminalErr = net.ErrClosed
	}
	l.mu.Unlock()

	if last {
		close(l.accepted)
	}
}

func (l *mergedListener) recordTerminalError(err error) {
	if err == nil {
		return
	}

	l.mu.Lock()
	l.terminalErr = errors.Join(l.terminalErr, err)
	l.mu.Unlock()
}

func (l *mergedListener) terminalErrorOr(fallback error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.terminalErr == nil {
		return fallback
	}
	return l.terminalErr
}
