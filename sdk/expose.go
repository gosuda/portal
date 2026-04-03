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

	identity   types.Identity
	TargetAddr string
	UDPAddr    string
	udpEnabled bool
	tcpEnabled bool
	banMITM    bool
	metadata   types.LeaseMetadata
	rootCAPEM  []byte

	accepted  chan net.Conn
	datagrams chan types.DatagramFrame

	relaySet       *discovery.RelaySet
	listenerMu     sync.RWMutex
	relayListeners map[string]*Listener

	closeOnce sync.Once
	connSeq   atomic.Uint64
}

type ExposeConfig struct {
	RelayURLs    []string
	IdentityPath string
	IdentityJSON string
	Name         string
	TargetAddr   string
	UDPAddr      string
	UDPEnabled   bool
	TCPEnabled   bool
	BanMITM      bool
	Discovery    bool
	Metadata     types.LeaseMetadata
	RootCAPEM    []byte
}

// Expose creates relay listeners for each normalized relay URL and exposes a
// dynamic listener hub for accepting traffic from all of them.
func Expose(ctx context.Context, cfg ExposeConfig) (*Exposure, error) {
	relayURLs, err := utils.ResolvePortalRelayURLs(ctx, cfg.RelayURLs, cfg.Discovery)
	if err != nil {
		return nil, err
	}

	identity, createdIdentity, err := utils.ResolveListenerIdentity(
		types.Identity{Name: cfg.Name},
		cfg.IdentityPath,
		cfg.IdentityJSON,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve identity: %w", err)
	}
	if createdIdentity {
		log.Info().
			Str("identity_path", strings.TrimSpace(cfg.IdentityPath)).
			Str("address", identity.Address).
			Msg("generated tunnel identity and saved it to disk")
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
		cancel:         cancel,
		done:           exposureCtx.Done(),
		identity:       identity,
		TargetAddr:     targetAddr,
		UDPAddr:        udpAddr,
		udpEnabled:     cfg.UDPEnabled,
		tcpEnabled:     cfg.TCPEnabled,
		banMITM:        cfg.BanMITM,
		metadata:       cfg.Metadata.Copy(),
		rootCAPEM:      append([]byte(nil), cfg.RootCAPEM...),
		accepted:       make(chan net.Conn, max(len(relayURLs)*defaultReadyTarget*2, 1)),
		datagrams:      make(chan types.DatagramFrame, max(len(relayURLs)*32, 1)),
		relaySet:       discovery.NewRelaySet(),
		relayListeners: make(map[string]*Listener, len(relayURLs)),
	}

	if len(relayURLs) > 0 {
		exposure.relaySet.SetBootstrapRelayURLs(relayURLs)
		if err := exposure.reconcileRelayListeners(true); err != nil {
			_ = exposure.Close()
			return nil, err
		}
	}

	if cfg.Discovery {
		go func() {
			_ = exposure.relaySet.RunLoop(exposureCtx, exposure.rootCAPEM, func() error {
				return exposure.reconcileRelayListeners(false)
			})
		}()
	}
	go func() {
		<-exposure.done
		_ = exposure.Close()
	}()

	return exposure, nil
}

func (e *Exposure) ActiveRelayURLs() []string {
	if e == nil {
		return nil
	}
	if e.relaySet == nil {
		return nil
	}
	return e.relaySet.ActiveRelayURLs()
}

func (e *Exposure) Addr() net.Addr {
	return listenerAddr("portal:exposure")
}

func (e *Exposure) Identity() types.Identity {
	if e == nil {
		return types.Identity{}
	}
	return e.identity.Copy()
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

func (e *Exposure) Close() error {
	var closeErr error
	e.closeOnce.Do(func() {
		if e.cancel != nil {
			e.cancel()
		}

		e.listenerMu.RLock()
		relayURLs := make([]string, 0, len(e.relayListeners))
		listeners := make([]*Listener, 0, len(e.relayListeners))
		for relayURL, listener := range e.relayListeners {
			relayURLs = append(relayURLs, relayURL)
			listeners = append(listeners, listener)
		}
		e.listenerMu.RUnlock()

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

func (e *Exposure) reconcileRelayListeners(failOnError bool) error {
	if e.relaySet == nil {
		e.relaySet = discovery.NewRelaySet()
	}
	e.listenerMu.Lock()
	if e.relayListeners == nil {
		e.relayListeners = make(map[string]*Listener)
	}
	activeRelayURLs := e.relaySet.ActiveRelayURLs()
	currentRelayURLs := make([]string, 0, len(e.relayListeners))
	for relayURL := range e.relayListeners {
		currentRelayURLs = append(currentRelayURLs, relayURL)
	}
	missingRelayURLs := utils.FilterRelayURLs(activeRelayURLs, currentRelayURLs)
	staleRelayURLs := utils.FilterRelayURLs(currentRelayURLs, activeRelayURLs)
	staleListeners := make([]*Listener, 0, len(staleRelayURLs))
	for _, relayURL := range staleRelayURLs {
		staleListeners = append(staleListeners, e.relayListeners[relayURL])
		delete(e.relayListeners, relayURL)
	}
	e.listenerMu.Unlock()

	for i, listener := range staleListeners {
		if listener == nil {
			continue
		}
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Warn().Err(err).Str("relay_url", staleRelayURLs[i]).Msg("close stale relay listener")
		}
	}
	for _, relayURL := range missingRelayURLs {
		listener, err := NewListener(context.Background(), relayURL, ListenerConfig{
			Identity:   e.identity.Copy(),
			UDPEnabled: e.udpEnabled,
			TCPEnabled: e.tcpEnabled,
			BanMITM:    e.banMITM,
			Metadata:   e.metadata.Copy(),
			RootCAPEM:  append([]byte(nil), e.rootCAPEM...),
			relaySet:   e.relaySet,
		})
		if err != nil {
			if failOnError {
				return fmt.Errorf("listen %q: %w", relayURL, err)
			}
			log.Warn().Err(err).Str("relay_url", relayURL).Msg("add relay listener")
			continue
		}

		select {
		case <-e.done:
			_ = listener.Close()
			continue
		default:
		}

		e.listenerMu.Lock()
		if e.relayListeners == nil {
			e.relayListeners = make(map[string]*Listener, 1)
		}
		if _, exists := e.relayListeners[relayURL]; exists {
			e.listenerMu.Unlock()
			_ = listener.Close()
			continue
		}
		e.relayListeners[relayURL] = listener
		e.listenerMu.Unlock()

		go e.runListenerAcceptLoop(listener)
	}
	return nil
}

func (e *Exposure) runListenerAcceptLoop(listener *Listener) {
	if listener == nil {
		return
	}

	relayURL := listener.api.baseURL.String()
	if e.udpEnabled {
		go func() {
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
						Str("address", listener.Address()).
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
	defer func() {
		e.listenerMu.Lock()
		if current, ok := e.relayListeners[relayURL]; ok && current == listener {
			delete(e.relayListeners, relayURL)
		}
		e.listenerMu.Unlock()
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

	e.listenerMu.RLock()
	listener := e.relayListeners[frame.RelayURL]
	e.listenerMu.RUnlock()
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
		e.listenerMu.RLock()
		listeners := make([]*Listener, 0, len(e.relayListeners))
		for _, listener := range e.relayListeners {
			listeners = append(listeners, listener)
		}
		e.listenerMu.RUnlock()

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

func (e *Exposure) RunHTTP(ctx context.Context, handler http.Handler, localAddr string) error {
	var relayListener net.Listener
	e.listenerMu.RLock()
	activeListeners := make([]*Listener, 0, len(e.relayListeners))
	for _, relayURL := range e.relaySet.ActiveRelayURLs() {
		listener, ok := e.relayListeners[relayURL]
		if !ok {
			continue
		}
		activeListeners = append(activeListeners, listener)
	}
	e.listenerMu.RUnlock()
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
