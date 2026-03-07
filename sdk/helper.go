package sdk

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

// RunHTTP serves one handler on the relay listener and, when localAddr is set,
// on the provided local HTTP address for app-local access.
func RunHTTP(ctx context.Context, relayListener net.Listener, handler http.Handler, localAddr string) error {
	relaySrv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: defaultRequestTimeout,
	}

	var localSrv *http.Server
	if strings.TrimSpace(localAddr) != "" {
		localSrv = &http.Server{
			Addr:              strings.TrimSpace(localAddr),
			Handler:           handler,
			ReadHeaderTimeout: defaultRequestTimeout,
		}
	}

	group, groupCtx := errgroup.WithContext(ctx)
	if localSrv != nil {
		group.Go(func() error {
			if err := localSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("serve local http: %w", err)
			}
			return nil
		})
	}
	group.Go(func() error {
		if err := relaySrv.Serve(relayListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve relay http: %w", err)
		}
		return nil
	})
	group.Go(func() error {
		<-groupCtx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultHTTPShutdownTimeout)
		defer cancel()

		var localErr error
		if localSrv != nil {
			localErr = localSrv.Shutdown(shutdownCtx)
			if errors.Is(localErr, http.ErrServerClosed) {
				localErr = nil
			}
		}

		relayErr := relaySrv.Shutdown(shutdownCtx)
		if errors.Is(relayErr, http.ErrServerClosed) {
			relayErr = nil
		}

		return errors.Join(localErr, relayErr)
	})

	return group.Wait()
}

// MergeListeners fans in multiple listeners into one net.Listener. It keeps
// serving accepts from remaining listeners when one listener stops, and returns
// a terminal error only after all source listeners have stopped.
func MergeListeners(listeners ...net.Listener) (net.Listener, error) {
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
		return conn, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.terminalErr == nil {
		return nil, net.ErrClosed
	}
	return nil, l.terminalErr
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

// SplitCSV splits a comma-separated string, trimming whitespace and dropping
// empty entries.
func SplitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
