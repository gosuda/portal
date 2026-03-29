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

	mu                     sync.RWMutex
	knownRelayURLs         []string
	bannedRelayURLs        []string
	discoveryPins          map[string]discoveryIdentity
	discoveryRelayIDsByURL map[string]string
	listeners              map[string]*Listener

	closeOnce sync.Once
	connSeq   atomic.Uint64
}

type discoveryIdentity struct {
	apiURL          string
	signerPublicKey string
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
		cancel:                 cancel,
		done:                   exposureCtx.Done(),
		name:                   cfg.Name,
		TargetAddr:             targetAddr,
		UDPAddr:                udpAddr,
		reverseToken:           cfg.ReverseToken,
		udpEnabled:             cfg.UDPEnabled,
		banMITM:                cfg.BanMITM,
		metadata:               cfg.Metadata.Copy(),
		ownerAddress:           identity.Address,
		rootCAPEM:              append([]byte(nil), cfg.RootCAPEM...),
		discoveryEnabled:       cfg.Discovery,
		accepted:               make(chan net.Conn, max(len(relayURLs)*defaultReadyTarget*2, 1)),
		datagrams:              make(chan types.DatagramFrame, max(len(relayURLs)*32, 1)),
		discoveryPins:          make(map[string]discoveryIdentity),
		discoveryRelayIDsByURL: make(map[string]string),
		listeners:              make(map[string]*Listener, len(relayURLs)),
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
		activeRelayURLs, _ := exposure.activeSnapshotLocked()
		exposure.mu.RUnlock()
		log.Info().
			Str("release_version", types.ReleaseVersion).
			Int("relay_count", len(activeRelayURLs)).
			Strs("relays", activeRelayURLs).
			Msg("exposure relay started")
	}

	return exposure, nil
}

func (e *Exposure) ActiveRelayURLs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if len(e.knownRelayURLs) == 0 || len(e.listeners) == 0 {
		return nil
	}

	activeRelayURLs := make([]string, 0, len(e.listeners))
	for _, relayURL := range e.knownRelayURLs {
		if _, ok := e.listeners[relayURL]; ok {
			activeRelayURLs = append(activeRelayURLs, relayURL)
		}
	}
	if len(activeRelayURLs) == 0 {
		return nil
	}
	return activeRelayURLs
}

func (e *Exposure) activeSnapshotLocked() ([]string, []*Listener) {
	if len(e.knownRelayURLs) == 0 || len(e.listeners) == 0 {
		return nil, nil
	}

	activeRelayURLs := make([]string, 0, len(e.listeners))
	listeners := make([]*Listener, 0, len(e.listeners))
	for _, relayURL := range e.knownRelayURLs {
		listener, ok := e.listeners[relayURL]
		if !ok {
			continue
		}
		activeRelayURLs = append(activeRelayURLs, relayURL)
		listeners = append(listeners, listener)
	}
	if len(activeRelayURLs) == 0 {
		return nil, nil
	}
	return activeRelayURLs, listeners
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
	_, activeListeners := e.activeSnapshotLocked()
	e.mu.RUnlock()
	if len(activeListeners) > 0 {
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
		relayURLs := make([]string, 0, len(e.listeners))
		listeners := make([]*Listener, 0, len(e.listeners))
		for relayURL, listener := range e.listeners {
			relayURLs = append(relayURLs, relayURL)
			listeners = append(listeners, listener)
		}
		e.mu.RUnlock()

		for _, listener := range listeners {
			if listener != nil {
				closeErr = errors.Join(closeErr, listener.Close())
			}
		}

		event := log.Info().
			Int("relay_count", len(listeners)).
			Strs("relays", relayURLs)
		if closeErr != nil {
			event = log.Warn().
				Err(closeErr).
				Int("relay_count", len(listeners)).
				Strs("relays", relayURLs)
		}
		event.Msg("exposure closed")
	})
	return closeErr
}

func (e *Exposure) setRelayURLs(relayURLs []string, failOnError bool) ([]string, error) {
	if len(relayURLs) == 0 {
		return nil, nil
	}

	e.mu.RLock()
	bannedRelayURLs := append([]string(nil), e.bannedRelayURLs...)
	e.mu.RUnlock()
	relayURLs = utils.FilterRelayURLs(append([]string(nil), relayURLs...), bannedRelayURLs)

	e.mu.Lock()
	existing := make(map[string]struct{}, len(e.knownRelayURLs))
	for _, relayURL := range e.knownRelayURLs {
		existing[relayURL] = struct{}{}
	}
	desired := make(map[string]struct{}, len(relayURLs))
	for _, relayURL := range relayURLs {
		desired[relayURL] = struct{}{}
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
	removed := make([]string, 0)
	staleListeners := make([]*Listener, 0)
	for relayURL, listener := range e.listeners {
		if _, ok := desired[relayURL]; ok {
			continue
		}
		removed = append(removed, relayURL)
		staleListeners = append(staleListeners, listener)
		delete(e.listeners, relayURL)
	}
	e.knownRelayURLs = append([]string(nil), relayURLs...)
	e.mu.Unlock()

	for i, listener := range staleListeners {
		if listener == nil {
			continue
		}
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Warn().Err(err).Str("relay_url", removed[i]).Msg("close stale relay listener")
		}
	}
	if len(removed) > 0 {
		log.Info().Int("removed_count", len(removed)).Strs("removed_relays", removed).Msg("stale relays removed from exposure")
	}

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

func (e *Exposure) pinDiscoverySelfDescriptor(targetURL string, desc types.RelayDescriptor) error {
	normalizedTargetURL, err := utils.NormalizeRelayURL(targetURL)
	if err != nil {
		return err
	}
	if desc.APIHTTPSAddr != normalizedTargetURL {
		return errors.New("descriptor api_https_addr does not match target url")
	}
	return e.pinDiscoveredDescriptor(desc)
}

func (e *Exposure) pinDiscoveredDescriptor(desc types.RelayDescriptor) error {
	relayID := strings.TrimSpace(desc.RelayID)
	apiURL := strings.TrimSpace(desc.APIHTTPSAddr)
	signerPublicKey := strings.TrimSpace(desc.SignerPublicKey)
	if relayID == "" {
		return errors.New("descriptor relay_id is required")
	}
	if apiURL == "" {
		return errors.New("descriptor api_https_addr is required")
	}
	if signerPublicKey == "" {
		return errors.New("descriptor signer_public_key is required")
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.discoveryPins == nil {
		e.discoveryPins = make(map[string]discoveryIdentity)
	}
	if e.discoveryRelayIDsByURL == nil {
		e.discoveryRelayIDsByURL = make(map[string]string)
	}

	if pinned, ok := e.discoveryPins[relayID]; ok {
		if pinned.apiURL != apiURL {
			return errors.New("descriptor api_https_addr does not match pinned relay url")
		}
		if pinned.signerPublicKey != signerPublicKey {
			return errors.New("descriptor signer_public_key does not match pinned signer")
		}
	}
	if pinnedRelayID, ok := e.discoveryRelayIDsByURL[apiURL]; ok && pinnedRelayID != relayID {
		return errors.New("descriptor relay_id does not match pinned relay url identity")
	}

	e.discoveryPins[relayID] = discoveryIdentity{
		apiURL:          apiURL,
		signerPublicKey: signerPublicKey,
	}
	e.discoveryRelayIDsByURL[apiURL] = relayID
	return nil
}

func (e *Exposure) banRelayURL(relayURL string) {
	e.mu.Lock()
	e.knownRelayURLs = utils.RemoveRelayURL(e.knownRelayURLs, relayURL)
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
	cfg := ListenerConfig{
		Name:         e.name,
		ReverseToken: e.reverseToken,
		UDPEnabled:   e.udpEnabled,
		BanMITM:      e.banMITM,
		Metadata:     e.metadata.Copy(),
		RootCAPEM:    append([]byte(nil), e.rootCAPEM...),
		ownerAddress: e.ownerAddress,
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
		_, listeners := e.activeSnapshotLocked()
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
	var (
		lastStatuses    map[string]listenerStatus
		lastBannedCount = -1
	)

	for {
		e.mu.RLock()
		_, listeners := e.activeSnapshotLocked()
		bannedCount := len(e.bannedRelayURLs)
		e.mu.RUnlock()

		currentStatuses := make(map[string]listenerStatus, len(listeners))
		readyCount := 0
		activated := make([]string, 0)
		deactivated := make([]string, 0)

		for _, listener := range listeners {
			if listener == nil {
				continue
			}
			relayURL := listener.api.baseURL.String()

			status := listener.StartupStatus()
			currentStatuses[relayURL] = status
			if status == listenerStatusReady {
				readyCount++
			}

			if lastStatuses != nil {
				if prev, ok := lastStatuses[relayURL]; ok && prev != status {
					if status == listenerStatusReady {
						activated = append(activated, relayURL)
					} else {
						deactivated = append(deactivated, relayURL)
					}
				}
			}
		}

		changed := lastStatuses == nil || bannedCount != lastBannedCount || len(currentStatuses) != len(lastStatuses)
		if !changed {
			for relayURL, status := range currentStatuses {
				if lastStatuses[relayURL] != status {
					changed = true
					break
				}
			}
		}
		if changed {
			inactiveCount := len(currentStatuses) - readyCount
			for relayURL, status := range lastStatuses {
				if _, ok := currentStatuses[relayURL]; ok || status != listenerStatusReady {
					continue
				}
				deactivated = append(deactivated, relayURL)
			}

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
			lastStatuses = currentStatuses
			lastBannedCount = bannedCount
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
		relayURLs := append([]string(nil), e.knownRelayURLs...)
		e.mu.RUnlock()
		if len(relayURLs) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}

		discoveredRelayURLs := append([]string(nil), relayURLs...)
		successCount := 0
		var discoveryErr error
		var warnErr error

		for _, relayURL := range relayURLs {
			resp, err := discovery.Discover(ctx, relayURL, types.DiscoverRequest{}, e.rootCAPEM, nil)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				discoveryErr = errors.Join(discoveryErr, fmt.Errorf("discover %q: %w", relayURL, err))
				continue
			}

			selfDescriptor, peerDescriptors, validateErr := discovery.ValidateResponse(resp, time.Now().UTC())
			if selfDescriptor.RelayID == "" || strings.TrimSpace(selfDescriptor.APIHTTPSAddr) == "" {
				discoveryErr = errors.Join(discoveryErr, fmt.Errorf("discover %q: missing self descriptor", relayURL))
				continue
			}
			if err := e.pinDiscoverySelfDescriptor(relayURL, selfDescriptor); err != nil {
				discoveryErr = errors.Join(discoveryErr, fmt.Errorf("discover %q: %w", relayURL, err))
				continue
			}
			if validateErr != nil {
				warnErr = errors.Join(warnErr, fmt.Errorf("discover %q: %w", relayURL, validateErr))
			}

			descriptorRelayURLs := make([]string, 0, 1+len(peerDescriptors))
			descriptorRelayURLs = append(descriptorRelayURLs, selfDescriptor.APIHTTPSAddr)
			for _, descriptor := range peerDescriptors {
				if err := e.pinDiscoveredDescriptor(descriptor); err != nil {
					warnErr = errors.Join(warnErr, fmt.Errorf("discover %q peer %q: %w", relayURL, descriptor.RelayID, err))
					continue
				}
				if apiURL := strings.TrimSpace(descriptor.APIHTTPSAddr); apiURL != "" {
					descriptorRelayURLs = append(descriptorRelayURLs, apiURL)
				}
			}

			discoveredRelayURLs, err = utils.MergeRelayURLs(discoveredRelayURLs, nil, descriptorRelayURLs)
			if err != nil {
				discoveryErr = errors.Join(discoveryErr, fmt.Errorf("merge %q discovery relays: %w", relayURL, err))
				continue
			}
			successCount++
		}

		switch {
		case successCount > 0:
			recovered := discoveryFailed
			discoveryFailed = false
			added, err := e.setRelayURLs(discoveredRelayURLs, false)
			if err != nil {
				log.Warn().
					Err(err).
					Int("relay_count", len(relayURLs)).
					Msg("relay discovery update failed")
			} else if recovered || len(added) > 0 || warnErr != nil || discoveryErr != nil {
				e.mu.RLock()
				totalKnownRelayCount := len(e.knownRelayURLs)
				e.mu.RUnlock()
				logErr := errors.Join(warnErr, discoveryErr)
				event := log.Info().
					Int("relay_count", len(relayURLs)).
					Int("discovered_count", successCount).
					Int("total_known_relay_count", totalKnownRelayCount)
				if recovered {
					event = event.Bool("recovered", true)
				}
				if len(added) > 0 {
					event = event.Int("added_count", len(added)).
						Strs("added_relays", added)
				}
				if logErr != nil {
					event = log.Warn().
						Err(logErr).
						Int("relay_count", len(relayURLs)).
						Int("discovered_count", successCount).
						Int("total_known_relay_count", totalKnownRelayCount)
					if recovered {
						event = event.Bool("recovered", true)
					}
					if len(added) > 0 {
						event = event.Int("added_count", len(added)).
							Strs("added_relays", added)
					}
				}
				event.Msg("relay discovery updated")
			}
		case ctx.Err() != nil:
			return
		default:
			if !discoveryFailed {
				log.Debug().
					Err(discoveryErr).
					Int("relay_count", len(relayURLs)).
					Msg("relay discovery failed")
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
