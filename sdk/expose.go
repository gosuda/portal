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

	identity, err := utils.ResolveSecp256k1Identity(cfg.OwnerPrivateKey)
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
		if _, err := exposure.setRelayURLs(relayURLs, true); err != nil {
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
		exposure.mu.RLock()
		activeRelayURLs := append([]string(nil), exposure.activeRelayURLs...)
		exposure.mu.RUnlock()
		log.Info().
			Str("release_version", types.ReleaseVersion).
			Int("relay_count", len(activeRelayURLs)).
			Strs("relays", activeRelayURLs).
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

func (e *Exposure) RunHTTP(ctx context.Context, handler http.Handler, localAddr string) error {
	var relayListener net.Listener
	e.mu.RLock()
	hasActiveRelays := len(e.activeRelayURLs) > 0
	e.mu.RUnlock()
	if hasActiveRelays {
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

		e.mu.RLock()
		listeners := make([]*Listener, 0, len(e.listeners))
		activeRelayURLs := append([]string(nil), e.activeRelayURLs...)
		for _, relayURL := range e.activeRelayURLs {
			if listener, ok := e.listeners[relayURL]; ok {
				listeners = append(listeners, listener)
			}
		}
		e.mu.RUnlock()

		for _, listener := range listeners {
			if listener != nil {
				closeErr = errors.Join(closeErr, listener.Close())
			}
		}

		event := log.Info().
			Int("relay_count", len(listeners)).
			Strs("relays", activeRelayURLs)
		if closeErr != nil {
			event = log.Warn().
				Err(closeErr).
				Int("relay_count", len(listeners)).
				Strs("relays", activeRelayURLs)
		}
		event.Msg("exposure closed")
	})
	return closeErr
}

func (e *Exposure) setRelayURLs(relayURLs []string, failOnError bool) ([]string, error) {
	if len(relayURLs) == 0 {
		return nil, nil
	}

	relayURLs = utils.FilterRelayURLs(append([]string(nil), relayURLs...), e.bannedRelayURLs)
	e.mu.Lock()
	existing := make(map[string]struct{}, len(e.knownRelayURLs))
	for _, relayURL := range e.knownRelayURLs {
		existing[relayURL] = struct{}{}
	}

	added := make([]string, 0, len(relayURLs))
	missing := make([]string, 0)
	for _, relayURL := range relayURLs {
		if _, ok := existing[relayURL]; !ok {
			added = append(added, relayURL)
		}
		if _, ok := e.listeners[relayURL]; !ok {
			missing = append(missing, relayURL)
		}
	}
	e.knownRelayURLs = append([]string(nil), relayURLs...)
	e.activeRelayURLs = append([]string(nil), relayURLs...)
	e.mu.Unlock()

	for _, relayURL := range missing {
		listener, err := e.newListener(relayURL)
		if err != nil {
			if failOnError {
				return nil, fmt.Errorf("listen %q: %w", relayURL, err)
			}
			log.Warn().Err(err).Str("relay_url", relayURL).Msg("add relay listener")
			continue
		}
		e.installListener(relayURL, listener)
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

func (e *Exposure) newListener(relayURL string) (*Listener, error) {
	bootstraps := []string(nil)
	if e.discoveryEnabled {
		e.mu.RLock()
		if len(e.knownRelayURLs) > 0 {
			bootstraps = append([]string(nil), e.knownRelayURLs...)
		}
		e.mu.RUnlock()
	}

	cfg := ListenerConfig{
		Name:               e.name,
		ReverseToken:       e.reverseToken,
		UDPEnabled:         e.udpEnabled,
		BanMITM:            e.banMITM,
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
		go func() {
			relayURL := listener.api.baseURL.String()
			for {
				frame, err := listener.AcceptDatagram()
				if err != nil {
					select {
					case <-e.done:
						return
					default:
					}
					if errors.Is(err, net.ErrClosed) {
						return
					}
					log.Warn().
						Err(err).
						Str("relay_url", relayURL).
						Str("lease_id", listener.LeaseID()).
						Msg("datagram accept failed")
					return
				}

				select {
				case <-e.done:
					return
				case e.datagrams <- frame:
				}
			}
		}()
	}
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

	e.mu.RLock()
	listener := e.listeners[frame.RelayURL]
	e.mu.RUnlock()
	if listener == nil {
		return net.ErrClosed
	}
	return listener.SendDatagram(frame)
}

func (e *Exposure) WaitDatagramReady(ctx context.Context) ([]string, error) {
	if !e.udpEnabled {
		return nil, errors.New("exposure does not have udp enabled")
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		e.mu.RLock()
		listeners := make([]*Listener, 0, len(e.listeners))
		for _, relayURL := range e.activeRelayURLs {
			if listener, ok := e.listeners[relayURL]; ok {
				listeners = append(listeners, listener)
			}
		}
		e.mu.RUnlock()

		addrs := make([]string, 0, len(listeners))
		seen := make(map[string]struct{})
		resolvedWithoutDatagram := true
		for _, listener := range listeners {
			if listener == nil {
				continue
			}

			udpAddr, ready, pending := listener.DatagramReady()
			if ready {
				if _, ok := seen[udpAddr]; !ok {
					seen[udpAddr] = struct{}{}
					addrs = append(addrs, udpAddr)
				}
			}
			if pending {
				resolvedWithoutDatagram = false
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

func (e *Exposure) monitorStartupCounts() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	prevStatuses := make(map[string]listenerStatus)
	firstRun := true

	for {
		e.mu.RLock()
		listeners := make([]*Listener, 0, len(e.listeners))
		for _, relayURL := range e.activeRelayURLs {
			if listener, ok := e.listeners[relayURL]; ok {
				listeners = append(listeners, listener)
			}
		}
		bannedCount := len(e.bannedRelayURLs)
		e.mu.RUnlock()

		readyCount, inactiveCount := 0, 0
		activated := make([]string, 0)
		deactivated := make([]string, 0)

		for _, listener := range listeners {
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
		e.mu.RLock()
		knownRelayURLs := append([]string(nil), e.knownRelayURLs...)
		e.mu.RUnlock()

		peers, err := utils.ExcludeLocalRelayURLs(knownRelayURLs...)
		if err == nil && len(peers) > 0 {
			relayURLs := append([]string(nil), peers...)
			var discoverErr error

			for _, peer := range peers {
				resp, err := discovery.Discover(ctx, peer, types.DiscoverRequest{}, e.rootCAPEM, nil)
				if err != nil {
					discoverErr = errors.Join(discoverErr, fmt.Errorf("discover %q: %w", peer, err))
					continue
				}

				now := time.Now().UTC()
				self, descriptors, err := discovery.ValidateResponse(resp, now)
				if err != nil {
					if self.RelayID == "" {
						discoverErr = errors.Join(discoverErr, fmt.Errorf("validate %q self descriptor: %w", peer, err))
						continue
					}
					discoverErr = errors.Join(discoverErr, fmt.Errorf("validate %q peer descriptors: %w", peer, err))
				}

				urls := make([]string, 0, 1+len(descriptors))
				if apiURL := strings.TrimSpace(self.APIHTTPSAddr); apiURL != "" {
					urls = append(urls, apiURL)
				}
				for _, descriptor := range descriptors {
					if apiURL := strings.TrimSpace(descriptor.APIHTTPSAddr); apiURL != "" {
						urls = append(urls, apiURL)
					}
				}

				discoveredRelayURLs, err := utils.ExcludeLocalRelayURLs(urls...)
				if err != nil {
					discoverErr = errors.Join(discoverErr, fmt.Errorf("extract %q relay urls: %w", peer, err))
					continue
				}
				relayURLs, err = utils.MergeRelayURLs(relayURLs, nil, discoveredRelayURLs)
				if err != nil {
					discoverErr = errors.Join(discoverErr, fmt.Errorf("merge %q relay urls: %w", peer, err))
					continue
				}
			}

			err = discoverErr
			switch {
			case err == nil:
				recovered := discoveryFailed
				discoveryFailed = false
				added, err := e.setRelayURLs(relayURLs, false)
				if err != nil {
					log.Warn().
						Err(err).
						Int("relay_count", len(peers)).
						Msg("discover relay urls failed")
				} else if recovered || len(added) > 0 {
					e.mu.RLock()
					totalKnownRelayCount := len(e.knownRelayURLs)
					e.mu.RUnlock()
					event := log.Info().
						Int("peer_count", len(peers)).
						Int("total_known_relay_count", totalKnownRelayCount)
					if recovered {
						event = event.Bool("recovered", true)
					}
					if len(added) > 0 {
						event = event.Int("added_count", len(added)).
							Strs("added_relays", added)
					}
					event.Msg("discovery relays updated")
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
		} else if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !discoveryFailed {
				log.Debug().
					Err(err).
					Msg("discover relay urls failed")
			}
			discoveryFailed = true
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
