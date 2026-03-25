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
	cancel context.CancelFunc
	done   <-chan struct{}

	name             string
	TargetAddr       string
	UDPAddr          string
	reverseToken     string
	udpEnabled       bool
	banMITM          bool
	metadata         types.LeaseMetadata
	ownerAddress     string
	rootCAPEM        []byte
	discoveryEnabled bool

	accepted  chan net.Conn
	datagrams chan types.DatagramFrame

	mu              sync.RWMutex
	knownRelayURLs  []string
	activeRelayURLs []string
	bannedRelayURLs []string
	listeners       map[string]*Listener

	closeOnce sync.Once
	connSeq   atomic.Uint64
}

type ExposeConfig struct {
	RelayURLs       []string
	Name            string
	TargetAddr      string
	UDPAddr         string
	ReverseToken    string
	UDPEnabled      bool
	BanMITM         bool
	Discovery       bool
	Metadata        types.LeaseMetadata
	OwnerPrivateKey string
	RootCAPEM       []byte
}

// Expose creates relay listeners for each normalized relay URL and exposes a
// dynamic listener hub for accepting traffic from all of them.
func Expose(ctx context.Context, cfg ExposeConfig) (*Exposure, error) {
	relayURLs, err := utils.ResolvePortalRelayURLs(ctx, cfg.RelayURLs, cfg.Discovery)
	if err != nil {
		return nil, err
	}

	identity, err := discovery.ResolveIdentity(cfg.OwnerPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("resolve owner identity: %w", err)
	}
	targetAddr, err := utils.NormalizeLoopbackTarget(cfg.TargetAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid target value %q: %w", cfg.TargetAddr, err)
	}
	udpAddr := cfg.UDPAddr
	if cfg.UDPEnabled {
		udpAddr, err = utils.NormalizeLoopbackTarget(utils.StringOrDefault(udpAddr, targetAddr))
		if err != nil {
			return nil, fmt.Errorf("invalid --udp-addr value %q: %w", cfg.UDPAddr, err)
		}
	}

	exposureCtx, cancel := context.WithCancel(ctx)
	exposure := &Exposure{
		cancel:           cancel,
		done:             exposureCtx.Done(),
		name:             cfg.Name,
		TargetAddr:       targetAddr,
		UDPAddr:          udpAddr,
		reverseToken:     cfg.ReverseToken,
		udpEnabled:       cfg.UDPEnabled,
		banMITM:          cfg.BanMITM,
		metadata:         cfg.Metadata.Copy(),
		ownerAddress:     identity.Address,
		rootCAPEM:        append([]byte(nil), cfg.RootCAPEM...),
		discoveryEnabled: cfg.Discovery,
		accepted:         make(chan net.Conn, max(len(relayURLs)*defaultReadyTarget*2, 1)),
		datagrams:        make(chan types.DatagramFrame, max(len(relayURLs)*32, 1)),
		listeners:        make(map[string]*Listener, len(relayURLs)),
	}

	if len(relayURLs) > 0 {
		if _, err := exposure.applyRelayURLs(relayURLs, true); err != nil {
			_ = exposure.Close()
			return nil, err
		}
	}

	if len(relayURLs) > 0 {
		go exposure.monitorStartupCounts()
	}
	if exposure.discoveryEnabled {
		go exposure.runDiscoveryLoop(exposureCtx)
	}
	go func() {
		<-exposure.done
		_ = exposure.Close()
	}()

	if len(relayURLs) > 0 {
		log.Info().
			Str("release_version", types.ReleaseVersion).
			Int("relay_count", len(exposure.ActiveRelayURLs())).
			Strs("relays", exposure.ActiveRelayURLs()).
			Msg("exposure relay started")
	}

	return exposure, nil
}

func (e *Exposure) KnownRelayURLs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if len(e.knownRelayURLs) == 0 {
		return nil
	}

	return append([]string(nil), e.knownRelayURLs...)
}

func (e *Exposure) ActiveRelayURLs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if len(e.activeRelayURLs) == 0 {
		return nil
	}

	return append([]string(nil), e.activeRelayURLs...)
}

func (e *Exposure) BannedRelayURLs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if len(e.bannedRelayURLs) == 0 {
		return nil
	}

	return append([]string(nil), e.bannedRelayURLs...)
}

func (e *Exposure) Accept() (net.Conn, error) {
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
	if len(e.ActiveRelayURLs()) > 0 {
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
	var closeErr error
	e.closeOnce.Do(func() {
		if e.cancel != nil {
			e.cancel()
		}

		listeners := e.listenersOrdered()
		for _, listener := range listeners {
			if listener != nil {
				closeErr = errors.Join(closeErr, listener.Close())
			}
		}

		event := log.Info().
			Int("relay_count", len(listeners)).
			Strs("relays", e.ActiveRelayURLs())
		if closeErr != nil {
			event = log.Warn().
				Err(closeErr).
				Int("relay_count", len(listeners)).
				Strs("relays", e.ActiveRelayURLs())
		}
		event.Msg("exposure closed")
	})
	return closeErr
}

func (e *Exposure) applyRelayURLs(relayURLs []string, failOnError bool) ([]string, error) {
	if len(relayURLs) == 0 {
		return nil, nil
	}

	snapshot := append([]string(nil), relayURLs...)

	e.mu.Lock()
	snapshot = utils.FilterRelayURLs(snapshot, e.bannedRelayURLs)
	existing := make(map[string]struct{}, len(e.knownRelayURLs))
	for _, relayURL := range e.knownRelayURLs {
		existing[relayURL] = struct{}{}
	}
	if strings.Join(e.knownRelayURLs, "\x00") != strings.Join(snapshot, "\x00") {
		e.knownRelayURLs = snapshot
	}
	if strings.Join(e.activeRelayURLs, "\x00") != strings.Join(snapshot, "\x00") {
		e.activeRelayURLs = snapshot
	}
	e.mu.Unlock()

	added := make([]string, 0, len(snapshot))
	for _, relayURL := range snapshot {
		if _, ok := existing[relayURL]; ok {
			continue
		}
		added = append(added, relayURL)
	}

	if err := e.syncListeners(failOnError); err != nil {
		return nil, err
	}
	return added, nil
}

func (e *Exposure) banRelayURL(relayURL string) {
	e.mu.Lock()
	e.knownRelayURLs = utils.RemoveRelayURL(e.knownRelayURLs, relayURL)
	e.activeRelayURLs = utils.RemoveRelayURL(e.activeRelayURLs, relayURL)
	e.bannedRelayURLs = utils.AppendUniqueRelayURL(e.bannedRelayURLs, relayURL)
	delete(e.listeners, relayURL)
	bannedRelayURLs := append([]string(nil), e.bannedRelayURLs...)
	e.mu.Unlock()

	log.Warn().
		Str("relay_url", relayURL).
		Strs("banned_relays", bannedRelayURLs).
		Msg("relay banned by mitm detection")
}

func (e *Exposure) syncListeners(failOnError bool) error {
	e.mu.Lock()
	missing := make([]string, 0)
	for _, relayURL := range e.activeRelayURLs {
		if _, ok := e.listeners[relayURL]; ok {
			continue
		}
		missing = append(missing, relayURL)
	}
	e.mu.Unlock()

	for _, relayURL := range missing {
		listener, err := e.newListener(relayURL)
		if err != nil {
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

func (e *Exposure) newListener(relayURL string) (*Listener, error) {
	bootstraps := []string(nil)
	if e.discoveryEnabled {
		bootstraps = e.KnownRelayURLs()
	}

	cfg := ListenerConfig{
		Name:               e.name,
		ReverseToken:       e.reverseToken,
		UDPEnabled:         e.udpEnabled,
		BanMITM:           e.banMITM,
		RegisterBootstraps: bootstraps,
		Metadata:           e.metadata.Copy(),
		RootCAPEM:          append([]byte(nil), e.rootCAPEM...),
		ownerAddress:       e.ownerAddress,
	}
	return NewListener(context.Background(), relayURL, cfg)
}

func (e *Exposure) installListener(relayURL string, listener *Listener) {
	if listener == nil {
		return
	}

	shouldClose := false
	e.mu.Lock()
	if e.closed() {
		shouldClose = true
	} else if _, exists := e.listeners[relayURL]; exists {
		shouldClose = true
	} else {
		e.listeners[relayURL] = listener
	}
	e.mu.Unlock()

	if shouldClose {
		_ = listener.Close()
		return
	}

	log.Info().Str("relay_url", relayURL).Msg("relay added to exposure")
	go e.runListenerAcceptLoop(listener)
	if e.udpEnabled {
		go func() {
			relayURL := listener.api.baseURL.String()
			if err := listener.WaitRegistered(context.Background()); err != nil {
				switch {
				case e.closed():
					return
				case errors.Is(err, net.ErrClosed), errors.Is(err, context.Canceled):
					return
				default:
					log.Warn().
						Err(err).
						Str("relay_url", relayURL).
						Msg("attach datagram plane failed")
					return
				}
			}
			if listener.UDPAddr() == "" {
				if !e.closed() && !listener.closed() {
					log.Warn().
						Str("relay_url", relayURL).
						Msg("attach datagram plane failed")
				}
				return
			}

			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for listener.datagram == nil || !listener.datagram.Connected() {
				select {
				case <-e.done:
					return
				case <-listener.doneCh:
					return
				case <-ticker.C:
				}
			}

			for {
				frame, err := listener.datagram.Accept(listener.doneCh)
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
		}()
	}
}

func (e *Exposure) listenersOrdered() []*Listener {
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make([]*Listener, 0, len(e.listeners))
	for _, relayURL := range e.activeRelayURLs {
		if listener, ok := e.listeners[relayURL]; ok {
			out = append(out, listener)
		}
	}
	return out
}

func (e *Exposure) runListenerAcceptLoop(listener *Listener) {
	if listener == nil {
		return
	}

	relayURL := listener.api.baseURL.String()
	defer func() {
		if listener.StartupStatus() == listenerStatusBanned {
			e.banRelayURL(relayURL)
			return
		}

		e.mu.Lock()
		if current, ok := e.listeners[relayURL]; ok && current == listener {
			delete(e.listeners, relayURL)
		}
		e.mu.Unlock()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if listener.closed() || errors.Is(err, net.ErrClosed) {
				return
			}
			log.Warn().Err(err).Str("relay_url", relayURL).Msg("exposure listener accept failed")
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

func (e *Exposure) AcceptDatagram() (types.DatagramFrame, error) {
	if !e.udpEnabled {
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
	if !e.udpEnabled {
		return net.ErrClosed
	}

	relayURL := strings.TrimSpace(frame.RelayURL)
	if relayURL == "" {
		return errors.New("relay url is required")
	}

	e.mu.RLock()
	listener := e.listeners[relayURL]
	e.mu.RUnlock()
	if listener == nil || listener.datagram == nil {
		return net.ErrClosed
	}
	if leaseID := strings.TrimSpace(frame.LeaseID); leaseID != "" && leaseID != listener.LeaseID() {
		return errors.New("datagram frame targets stale lease")
	}
	return listener.datagram.Send(frame.FlowID, frame.Payload)
}

func (e *Exposure) WaitDatagramReady(ctx context.Context) ([]string, error) {
	if !e.udpEnabled {
		return nil, errors.New("exposure does not have udp enabled")
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		listeners := e.listenersOrdered()
		addrs := make([]string, 0, len(listeners))
		seen := make(map[string]struct{})
		resolvedWithoutDatagram := true
		for _, listener := range listeners {
			if listener == nil {
				continue
			}

			udpAddr := listener.UDPAddr()
			if listener.datagram != nil && listener.datagram.Connected() && udpAddr != "" {
				if _, ok := seen[udpAddr]; !ok {
					seen[udpAddr] = struct{}{}
					addrs = append(addrs, udpAddr)
				}
			}

			select {
			case <-listener.registered:
				if udpAddr != "" {
					resolvedWithoutDatagram = false
				}
			default:
				if !listener.closed() {
					resolvedWithoutDatagram = false
				}
			}
		}
		if len(addrs) > 0 {
			return addrs, nil
		}
		if resolvedWithoutDatagram {
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

func (e *Exposure) closed() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

func (e *Exposure) monitorStartupCounts() {
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
			relayURL := listener.api.baseURL.String()

			status := listener.StartupStatus()
			if status == listenerStatusReady {
				readyCount++
			} else {
				inactiveCount++
			}

			if prev, ok := prevStatuses[relayURL]; ok && prev != status {
				if status == listenerStatusReady {
					activated = append(activated, relayURL)
				} else {
					deactivated = append(deactivated, relayURL)
				}
			}
			prevStatuses[relayURL] = status
		}

		if firstRun || len(activated) > 0 || len(deactivated) > 0 {
			bannedCount := len(e.BannedRelayURLs())
			event := log.Info().
				Int("banned", bannedCount).
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
		case <-ticker.C:
		}
	}
}

const defaultDiscoveryInterval = 30 * time.Second

func (e *Exposure) runDiscoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(defaultDiscoveryInterval)
	defer ticker.Stop()
	discoveryFailed := false

	for {
		peers := e.KnownRelayURLs()
		if len(peers) > 0 {
			relayURLs, err := discovery.DiscoverBootstraps(ctx, peers, types.DiscoverRequest{}, e.rootCAPEM)
			switch {
			case err == nil:
				if discoveryFailed {
					log.Info().
						Int("peer_count", len(peers)).
						Msg("relay discovery recovered")
				}
				discoveryFailed = false
				added, err := e.applyRelayURLs(relayURLs, false)
				if err != nil {
					log.Warn().
						Err(err).
						Int("relay_count", len(peers)).
						Msg("apply discovered relay urls failed")
				} else if len(added) > 0 {
					log.Info().
						Int("peer_count", len(peers)).
						Int("added_count", len(added)).
						Int("total_known_relay_count", len(e.KnownRelayURLs())).
						Strs("added_relays", added).
						Msg("discovery relays updated")
				}
			case ctx.Err() != nil:
				return
			default:
				if !discoveryFailed {
					log.Debug().
						Err(err).
						Int("relay_count", len(peers)).
						Msg("discover relay urls failed")
				}
				discoveryFailed = true
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
