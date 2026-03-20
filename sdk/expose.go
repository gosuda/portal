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

	"github.com/gosuda/portal/v2/portal/discovery"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

// Exposure owns the lifecycle of one or more relay listeners and accepts
// traffic from all of them through one net.Listener.
type Exposure struct {
	ctx    context.Context
	cancel context.CancelFunc

	name             string
	reverseToken     string
	udpEnabled       bool
	identity         discovery.Identity
	metadata         types.LeaseMetadata
	ownerAddress     string
	rootCAPEM        []byte
	discoveryEnabled bool
	discovery        *discovery.Service

	accepted  chan net.Conn
	datagrams chan types.DatagramFrame
	done      chan struct{}

	mu        sync.RWMutex
	relayURLs []string
	listeners map[string]*Listener
	starting  map[string]struct{}

	closeOnce sync.Once
	connSeq   atomic.Uint64
}

type ExposeConfig struct {
	RelayURLs       []string
	Name            string
	ReverseToken    string
	UDPEnabled      bool
	Discovery       bool
	Metadata        types.LeaseMetadata
	OwnerAddress    string
	OwnerPrivateKey *string
	RootCAPEM       []byte
}

// Expose creates relay listeners for each normalized relay URL and exposes a
// dynamic listener hub for accepting traffic from all of them.
func Expose(ctx context.Context, relayURLs []string, name string, udpEnabled bool, metadata types.LeaseMetadata) (*Exposure, error) {
	return ExposeWithConfig(ctx, ExposeConfig{
		RelayURLs:  relayURLs,
		Name:       name,
		UDPEnabled: udpEnabled,
		Metadata:   metadata,
	})
}

func ExposeWithConfig(ctx context.Context, cfg ExposeConfig) (*Exposure, error) {
	relayURLs, err := utils.NormalizeRelayURLs(cfg.RelayURLs)
	if err != nil {
		return nil, err
	}
	if len(relayURLs) == 0 {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	ownerAddress := strings.TrimSpace(cfg.OwnerAddress)
	identity := discovery.Identity{}
	if cfg.OwnerPrivateKey != nil {
		identity, err = discovery.ResolveIdentity(*cfg.OwnerPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("resolve owner identity: %w", err)
		}
		ownerAddress = identity.Address
	}

	exposureCtx, cancel := context.WithCancel(ctx)
	exposure := &Exposure{
		ctx:              exposureCtx,
		cancel:           cancel,
		name:             cfg.Name,
		reverseToken:     cfg.ReverseToken,
		udpEnabled:       cfg.UDPEnabled,
		identity:         identity,
		metadata:         cfg.Metadata.Copy(),
		ownerAddress:     ownerAddress,
		rootCAPEM:        append([]byte(nil), cfg.RootCAPEM...),
		discoveryEnabled: cfg.Discovery,
		accepted:         make(chan net.Conn, max(len(relayURLs)*defaultReadyTarget*2, 1)),
		datagrams:        make(chan types.DatagramFrame, max(len(relayURLs)*32, 1)),
		done:             make(chan struct{}),
		listeners:        make(map[string]*Listener, len(relayURLs)),
		starting:         make(map[string]struct{}, len(relayURLs)),
	}

	if exposure.discoveryEnabled {
		service, err := discovery.New(discovery.Config{
			RootCAPEM: exposure.rootCAPEM,
			OnBootstraps: func(relays []string) {
				if err := exposure.applyRelayURLs(relays, false); err != nil {
					log.Warn().Err(err).Strs("relays", relays).Msg("apply discovered relay urls")
				}
			},
		}, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		exposure.discovery = service
	}

	if exposure.discovery != nil {
		if err := exposure.discovery.MergeBootstraps(relayURLs); err != nil {
			_ = exposure.Close()
			return nil, err
		}
		if err := exposure.applyRelayURLs(exposure.discovery.Bootstraps(), true); err != nil {
			_ = exposure.Close()
			return nil, err
		}
	} else if err := exposure.applyRelayURLs(relayURLs, true); err != nil {
		_ = exposure.Close()
		return nil, err
	}

	go exposure.monitorStartupCounts(exposureCtx)
	if exposure.discovery != nil {
		go func() {
			_ = exposure.discovery.RunPollLoop(exposureCtx, 0, types.DiscoverRequest{})
		}()
	}

	log.Info().
		Str("release_version", types.ReleaseVersion).
		Int("relay_count", len(exposure.RelayURLs())).
		Strs("relays", exposure.RelayURLs()).
		Msg("exposure relay started")

	return exposure, nil
}

func (e *Exposure) RelayURLs() []string {
	if e == nil {
		return nil
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	if len(e.relayURLs) == 0 {
		return nil
	}

	return append([]string(nil), e.relayURLs...)
}

func (e *Exposure) OwnerIdentity() discovery.Identity {
	if e == nil {
		return discovery.Identity{}
	}
	return e.identity
}

func (e *Exposure) Accept() (net.Conn, error) {
	if e == nil {
		return nil, net.ErrClosed
	}

	select {
	case <-e.done:
		return nil, net.ErrClosed
	case conn := <-e.accepted:
		if conn == nil {
			return nil, net.ErrClosed
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
}

func (e *Exposure) Addr() net.Addr {
	return listenerAddr("portal:exposure")
}

func (e *Exposure) PublicURLs() []string {
	listeners := e.listenersOrdered()
	if len(listeners) == 0 {
		return nil
	}

	out := make([]string, 0, len(listeners))
	seen := make(map[string]struct{})
	for _, listener := range listeners {
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
	var relayListener net.Listener
	if e != nil {
		relayListener = e
	}
	return RunHTTP(ctx, relayListener, handler, localAddr)
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

func (e *Exposure) Close() error {
	if e == nil {
		return nil
	}

	var closeErr error
	e.closeOnce.Do(func() {
		if e.cancel != nil {
			e.cancel()
		}
		if e.done != nil {
			close(e.done)
		}

		listeners := e.listenersOrdered()
		for _, listener := range listeners {
			if listener != nil {
				closeErr = errors.Join(closeErr, listener.Close())
			}
		}

		event := log.Info().
			Int("relay_count", len(listeners)).
			Strs("relays", e.RelayURLs())
		if closeErr != nil {
			event = log.Warn().
				Err(closeErr).
				Int("relay_count", len(listeners)).
				Strs("relays", e.RelayURLs())
		}
		event.Msg("exposure closed")
	})
	return closeErr
}

func (e *Exposure) applyRelayURLs(relayURLs []string, failOnError bool) error {
	if e == nil || len(relayURLs) == 0 {
		return nil
	}

	snapshot := append([]string(nil), relayURLs...)

	e.mu.Lock()
	if strings.Join(e.relayURLs, "\x00") != strings.Join(snapshot, "\x00") {
		e.relayURLs = snapshot
	}
	e.mu.Unlock()
	return e.syncListeners(failOnError)
}

func (e *Exposure) syncListeners(failOnError bool) error {
	if e == nil {
		return nil
	}

	missing := e.reserveMissingRelayURLs()
	for _, relayURL := range missing {
		listener, err := e.newListener(relayURL)
		if err != nil {
			e.mu.Lock()
			delete(e.starting, relayURL)
			e.mu.Unlock()
			if failOnError {
				return fmt.Errorf("listen %q: %w", relayURL, err)
			}
			log.Warn().Err(err).Str("relay_url", relayURL).Msg("add relay listener")
			continue
		}
		e.installListener(relayURL, listener)
	}
	return nil
}

func (e *Exposure) reserveMissingRelayURLs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	missing := make([]string, 0)
	for _, relayURL := range e.relayURLs {
		if _, ok := e.listeners[relayURL]; ok {
			continue
		}
		if _, ok := e.starting[relayURL]; ok {
			continue
		}
		e.starting[relayURL] = struct{}{}
		missing = append(missing, relayURL)
	}
	return missing
}

func (e *Exposure) newListener(relayURL string) (*Listener, error) {
	cfg := ListenerConfig{
		Name:             e.name,
		ReverseToken:     e.reverseToken,
		UDPEnabled:       e.udpEnabled,
		Discovery:        e.discoveryEnabled,
		OwnerAddress:     e.ownerAddress,
		Metadata:         e.metadata.Copy(),
		RootCAPEM:        append([]byte(nil), e.rootCAPEM...),
		bootstrapService: e.discovery,
	}
	return NewListener(e.ctx, relayURL, cfg)
}

func (e *Exposure) installListener(relayURL string, listener *Listener) {
	if e == nil || listener == nil {
		return
	}

	shouldClose := false
	e.mu.Lock()
	delete(e.starting, relayURL)
	select {
	case <-e.done:
		shouldClose = true
	default:
		if _, exists := e.listeners[relayURL]; exists {
			shouldClose = true
		} else {
			e.listeners[relayURL] = listener
		}
	}
	e.mu.Unlock()

	if shouldClose {
		_ = listener.Close()
		return
	}

	log.Info().Str("relay_url", relayURL).Msg("relay added to exposure")
	go e.runListenerAcceptLoop(listener)
	if e.udpEnabled {
		go e.attachDatagramPlane(e.ctx, listener)
	}
}

func (e *Exposure) listenersOrdered() []*Listener {
	if e == nil {
		return nil
	}

	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make([]*Listener, 0, len(e.listeners))
	for _, relayURL := range e.relayURLs {
		if listener, ok := e.listeners[relayURL]; ok {
			out = append(out, listener)
		}
	}
	return out
}

func (e *Exposure) listenerForRelayURL(relayURL string) *Listener {
	if e == nil {
		return nil
	}

	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.listeners[relayURL]
}

func (e *Exposure) runListenerAcceptLoop(listener *Listener) {
	if e == nil || listener == nil {
		return
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			if listener.done() || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Warn().Err(err).Str("relay_url", listener.relayURL).Msg("exposure listener accept failed")
			return
		}

		select {
		case <-e.done:
			_ = conn.Close()
			return
		case e.accepted <- conn:
		}
	}
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

func (e *Exposure) SupportsDatagram() bool {
	return e != nil && e.udpEnabled
}

func (e *Exposure) UDPEnabled() bool {
	return e != nil && e.udpEnabled
}

func (e *Exposure) AcceptDatagram() (types.DatagramFrame, error) {
	if e == nil || !e.SupportsDatagram() {
		return types.DatagramFrame{}, net.ErrClosed
	}

	select {
	case <-e.done:
		return types.DatagramFrame{}, net.ErrClosed
	case frame := <-e.datagrams:
		return frame, nil
	}
}

func (e *Exposure) SendDatagram(frame types.DatagramFrame) error {
	if e == nil || !e.SupportsDatagram() {
		return net.ErrClosed
	}

	relayURL := strings.TrimSpace(frame.RelayURL)
	if relayURL == "" {
		return errors.New("relay url is required")
	}

	listener := e.listenerForRelayURL(relayURL)
	if listener == nil {
		return net.ErrClosed
	}
	if leaseID := strings.TrimSpace(frame.LeaseID); leaseID != "" && leaseID != listener.LeaseID() {
		return errors.New("datagram frame targets stale lease")
	}
	return listener.SendDatagram(frame.FlowID, frame.Payload)
}

func (e *Exposure) UDPAddrs() []string {
	listeners := e.listenersOrdered()
	if len(listeners) == 0 || !e.SupportsDatagram() {
		return nil
	}

	out := make([]string, 0, len(listeners))
	seen := make(map[string]struct{})
	for _, listener := range listeners {
		if listener == nil {
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
	listeners := e.listenersOrdered()
	if len(listeners) == 0 || !e.SupportsDatagram() {
		return nil
	}

	out := make([]string, 0, len(listeners))
	seen := make(map[string]struct{})
	for _, listener := range listeners {
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
	listeners := e.listenersOrdered()
	if len(listeners) == 0 {
		return true
	}

	resolved := 0
	for _, listener := range listeners {
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

	return resolved == len(listeners)
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
		frame, err := listener.AcceptDatagram()
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

		frame.Payload = append([]byte(nil), frame.Payload...)
		frame.LeaseID = listener.LeaseID()
		frame.RelayURL = relayURL
		frame.UDPAddr = listener.UDPAddr()

		select {
		case <-e.done:
			return
		case e.datagrams <- frame:
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
	prevStatuses := make(map[string]listenerStatus)
	firstRun := true

	for {
		readyCount, inactiveCount := 0, 0
		activated := make([]string, 0)
		deactivated := make([]string, 0)

		for _, listener := range e.listenersOrdered() {
			if listener == nil {
				continue
			}

			status := listener.StartupStatus()
			if status == listenerStatusReady {
				readyCount++
			} else {
				inactiveCount++
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
