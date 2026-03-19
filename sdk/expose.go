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
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

// Exposure owns the lifecycle of one or more relay listeners and accepts
// traffic from all of them through one net.Listener.
type Exposure struct {
	udpEnabled bool
	listener   net.Listener
	listeners  []*Listener
	datagrams  chan exposureDatagram
	done       chan struct{}

	closeOnce sync.Once
	connSeq   atomic.Uint64
}

type exposureDatagram struct {
	FlowID   uint32
	LeaseID  string
	Payload  []byte
	RelayURL string
	UDPAddr  string

	reply func([]byte) error
}

func (e *Exposure) SupportsStream() bool {
	return e != nil
}

// Expose creates relay listeners for each normalized relay URL and exposes a
// merged listener for accepting traffic from all of them. Empty relay input
// returns nil, nil so callers can fall back to local-only serving.
func Expose(ctx context.Context, relayUrls []string, name string, udpEnabled bool, metadata types.LeaseMetadata) (*Exposure, error) {
	relayURLs, err := utils.NormalizeRelayURLs(relayUrls)
	if err != nil {
		return nil, err
	}
	if len(relayURLs) == 0 {
		return nil, nil
	}
	listeners := make([]*Listener, 0, len(relayURLs))
	cleanup := func() error {
		var closeErr error
		for _, listener := range listeners {
			if listener != nil {
				closeErr = errors.Join(closeErr, listener.Close())
			}
		}
		return closeErr
	}

	for _, relayURL := range relayURLs {
		listener, err := NewListener(ctx, relayURL, ListenerConfig{
			Name:       name,
			UDPEnabled: udpEnabled,
			Metadata:   metadata,
		})
		if err != nil {
			return nil, errors.Join(fmt.Errorf("listen %q: %w", relayURL, err), cleanup())
		}

		listeners = append(listeners, listener)
	}

	mergedListeners := make([]net.Listener, 0, len(listeners))
	for _, listener := range listeners {
		mergedListeners = append(mergedListeners, listener)
	}
	merged, err := mergeListeners(mergedListeners...)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("merge listeners: %w", err), cleanup())
	}

	exposure := &Exposure{
		udpEnabled: udpEnabled,
		listener:   merged,
		listeners:  listeners,
		datagrams:  make(chan exposureDatagram, max(len(listeners)*32, 1)),
		done:       make(chan struct{}),
	}
	go exposure.monitorStartupCounts(ctx)
	if exposure.UDPEnabled() {
		exposure.attachDatagramPlanes(ctx)
	}

	log.Info().
		Str("release_version", types.ReleaseVersion).
		Int("relay_count", len(exposure.listeners)).
		Strs("relays", exposure.RelayURLs()).
		Msgf("exposure relay started")

	return exposure, nil
}

// RelayURLs returns the normalized relay URLs backing the exposure.
func (e *Exposure) RelayURLs() []string {
	if e == nil || len(e.listeners) == 0 {
		return nil
	}

	out := make([]string, 0, len(e.listeners))
	for _, listener := range e.listeners {
		if listener == nil {
			continue
		}
		out = append(out, listener.relayURL)
	}
	return out
}

func (e *Exposure) Accept() (net.Conn, error) {
	if e == nil || e.listener == nil {
		return nil, net.ErrClosed
	}

	conn, err := e.listener.Accept()
	if err != nil {
		if !errors.Is(err, net.ErrClosed) {
			log.Warn().
				Err(err).
				Str("local_addr", utils.AddrString(e.listener.Addr())).
				Msg("exposure accept failed")
		}
		return nil, err
	}

	connID := e.connSeq.Add(1)
	log.Info().
		Uint64("conn_id", connID).
		Str("local_addr", utils.AddrString(conn.LocalAddr())).
		Str("remote_addr", utils.AddrString(conn.RemoteAddr())).
		Msg("exposure connection accepted")

	return &exposureConn{
		Conn:       conn,
		id:         connID,
		localAddr:  utils.AddrString(conn.LocalAddr()),
		remoteAddr: utils.AddrString(conn.RemoteAddr()),
	}, nil
}

func (e *Exposure) Addr() net.Addr {
	if e == nil || e.listener == nil {
		return listenerAddr("portal:exposure")
	}
	return e.listener.Addr()
}

func (e *Exposure) PublicURLs() []string {
	if e == nil || len(e.listeners) == 0 {
		return nil
	}

	out := make([]string, 0, len(e.listeners))
	seen := make(map[string]struct{})
	for _, listener := range e.listeners {
		if listener == nil {
			continue
		}
		rawURL := listener.PublicURL()
		if rawURL == "" {
			continue
		}
		if _, ok := seen[rawURL]; ok {
			continue
		}
		seen[rawURL] = struct{}{}
		out = append(out, rawURL)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (e *Exposure) RunHTTP(ctx context.Context, handler http.Handler, localAddr string) error {
	if e == nil || e.listener == nil {
		return RunHTTP(ctx, nil, handler, localAddr)
	}
	return RunHTTP(ctx, e, handler, localAddr)
}

func RunHTTP(ctx context.Context, relayListener net.Listener, handler http.Handler, localAddr string) error {
	localAddr = strings.TrimSpace(localAddr)

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

		event := log.Info().
			Uint64("conn_id", c.id).
			Str("local_addr", c.localAddr).
			Str("remote_addr", c.remoteAddr)
		if closeErr != nil {
			event = log.Warn().
				Err(closeErr).
				Uint64("conn_id", c.id).
				Str("local_addr", c.localAddr).
				Str("remote_addr", c.remoteAddr)
		}
		event.Msg("exposure connection closed")
	})
	return closeErr
}

// Close closes the merged listener and all underlying relay listeners.
func (e *Exposure) Close() error {
	if e == nil {
		return nil
	}

	var closeErr error
	e.closeOnce.Do(func() {
		if e.done != nil {
			close(e.done)
		}
		if e.listener != nil {
			closeErr = errors.Join(closeErr, e.listener.Close())
		}
		for _, listener := range e.listeners {
			if listener != nil {
				closeErr = errors.Join(closeErr, listener.Close())
			}
		}

		event := log.Info().
			Int("relay_count", len(e.listeners)).
			Strs("relays", e.RelayURLs())
		if closeErr != nil {
			event = log.Warn().
				Err(closeErr).
				Int("relay_count", len(e.listeners)).
				Strs("relays", e.RelayURLs())
		}
		event.Msg("exposure closed")
	})
	return closeErr
}

func (e *Exposure) SupportsDatagram() bool {
	return e != nil && e.udpEnabled
}

func (e *Exposure) UDPEnabled() bool {
	return e != nil && e.udpEnabled
}

func (e *Exposure) AcceptDatagram() (types.DatagramFrame, string, string, string, func([]byte) error, error) {
	if e == nil || !e.SupportsDatagram() {
		return types.DatagramFrame{}, "", "", "", nil, net.ErrClosed
	}

	select {
	case <-e.done:
		return types.DatagramFrame{}, "", "", "", nil, net.ErrClosed
	case dg := <-e.datagrams:
		reply := dg.reply
		if reply == nil {
			reply = func([]byte) error { return errors.New("reply path is unavailable") }
		}
		return types.DatagramFrame{
			FlowID:  dg.FlowID,
			Payload: dg.Payload,
		}, dg.LeaseID, dg.RelayURL, dg.UDPAddr, reply, nil
	}
}

func (e *Exposure) UDPAddrs() []string {
	if e == nil || len(e.listeners) == 0 || !e.SupportsDatagram() {
		return nil
	}

	out := make([]string, 0, len(e.listeners))
	seen := make(map[string]struct{})
	for _, listener := range e.listeners {
		if listener == nil {
			continue
		}

		listener.mu.Lock()
		udpAddr := listener.udpAddr
		listener.mu.Unlock()
		if udpAddr == "" {
			continue
		}
		if _, ok := seen[udpAddr]; ok {
			continue
		}
		seen[udpAddr] = struct{}{}
		out = append(out, udpAddr)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (e *Exposure) WaitDatagramReady(ctx context.Context) ([]string, error) {
	if e == nil || !e.SupportsDatagram() {
		return nil, errors.New("exposure does not have udp enabled")
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if addrs := e.readyUDPAddrs(); len(addrs) > 0 {
			return addrs, nil
		}
		if e.allDatagramNegotiationsResolvedWithoutDatagram() {
			return nil, errors.New("relay did not expose udp")
		}

		select {
		case <-e.done:
			return nil, net.ErrClosed
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (e *Exposure) readyUDPAddrs() []string {
	if e == nil || len(e.listeners) == 0 || !e.SupportsDatagram() {
		return nil
	}

	out := make([]string, 0, len(e.listeners))
	seen := make(map[string]struct{})
	for _, listener := range e.listeners {
		if listener == nil || !listener.datagramConnected() {
			continue
		}

		udpAddr := listener.UDPAddr()
		if udpAddr == "" {
			continue
		}
		if _, ok := seen[udpAddr]; ok {
			continue
		}
		seen[udpAddr] = struct{}{}
		out = append(out, udpAddr)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (e *Exposure) allDatagramNegotiationsResolvedWithoutDatagram() bool {
	if e == nil || len(e.listeners) == 0 {
		return true
	}

	resolved := 0
	for _, listener := range e.listeners {
		if listener == nil {
			resolved++
			continue
		}

		registered, enabled := listener.datagramNegotiationState()
		if !registered {
			if listener.done() {
				resolved++
			}
			continue
		}
		if enabled {
			return false
		}
		resolved++
	}

	return resolved == len(e.listeners)
}

func (e *Exposure) attachDatagramPlanes(ctx context.Context) {
	for _, listener := range e.listeners {
		if listener == nil {
			continue
		}

		go e.attachDatagramPlane(ctx, listener)
	}
}

func (e *Exposure) attachDatagramPlane(ctx context.Context, listener *Listener) {
	err := listener.WaitDatagramReady(ctx)
	if err != nil {
		switch {
		case e.closed():
			return
		case ctx != nil && ctx.Err() != nil:
			return
		case errors.Is(err, net.ErrClosed), errors.Is(err, context.Canceled):
			return
		default:
			log.Warn().
				Err(err).
				Str("relay_url", listener.relayURL).
				Msg("attach datagram plane failed")
			return
		}
	}

	e.forwardDatagrams(listener.relayURL, listener)
}

func (e *Exposure) forwardDatagrams(relayURL string, listener *Listener) {
	for {
		dg, err := listener.AcceptDatagram()
		if err != nil {
			if e.closed() || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Warn().
				Err(err).
				Str("relay_url", relayURL).
				Str("lease_id", listener.LeaseID()).
				Msg("datagram accept failed")
			return
		}

		flowID := dg.FlowID
		reply := func(payload []byte) error {
			return listener.SendDatagram(flowID, payload)
		}

		select {
		case <-e.done:
			return
		case e.datagrams <- exposureDatagram{
			FlowID:   flowID,
			LeaseID:  listener.LeaseID(),
			Payload:  append([]byte(nil), dg.Payload...),
			RelayURL: relayURL,
			UDPAddr:  listener.UDPAddr(),
			reply:    reply,
		}:
		}
	}
}

func (e *Exposure) closed() bool {
	if e == nil || e.done == nil {
		return true
	}

	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

func (e *Exposure) monitorStartupCounts(ctx context.Context) {
	if e == nil {
		return
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	prevStatuses := make(map[string]listenerStatus, len(e.listeners))
	firstRun := true

	for {
		readyCount, inactiveCount := 0, 0
		activated := make([]string, 0)
		deactivated := make([]string, 0)
		for _, listener := range e.listeners {
			status := listenerStatusInactive
			if listener != nil {
				status = listener.StartupStatus()
			}
			if status == listenerStatusReady {
				readyCount++
			} else {
				inactiveCount++
			}

			if listener == nil {
				continue
			}
			if prev, ok := prevStatuses[listener.relayURL]; ok && prev != status {
				if status == listenerStatusReady {
					activated = append(activated, listener.relayURL)
				} else {
					deactivated = append(deactivated, listener.relayURL)
				}
			}
			prevStatuses[listener.relayURL] = status
		}

		if firstRun || len(activated) > 0 || len(deactivated) > 0 {
			event := log.Info().
				Int("inactive", inactiveCount).
				Int("ready", readyCount)
			if len(activated) > 0 {
				event = event.Strs("activated", activated)
			}
			if len(deactivated) > 0 {
				event = event.Strs("deactivated", deactivated)
			}
			event.Msg("relay status")
			firstRun = false
		}

		select {
		case <-e.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
