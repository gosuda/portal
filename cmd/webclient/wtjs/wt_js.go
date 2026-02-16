package wtjs

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall/js"

	"gosuda.org/portal/portal"
)

var (
	ErrConnectionFailed = errors.New("webtransport: connection failed")
	ErrSessionClosed    = errors.New("webtransport: session closed")
)

// Session wraps a browser WebTransport object and implements portal.Session.
type Session struct {
	transport js.Value

	incoming  chan portal.Stream
	closeCh   chan struct{}
	closeOnce sync.Once

	// funcs holds all js.Func values to release on close.
	funcsMu sync.Mutex
	funcs   []js.Func
}

var _ portal.Session = (*Session)(nil)

// Dial creates a WebTransport connection to the given URL.
// certHashes provides SHA-256 certificate hashes for the browser's
// serverCertificateHashes option (used with self-signed dev certs).
func Dial(url string, certHashes [][]byte) (*Session, error) {
	wtClass := js.Global().Get("WebTransport")
	if wtClass.IsUndefined() {
		return nil, errors.New("webtransport: WebTransport API not available in this browser")
	}

	// Build options
	opts := js.Global().Get("Object").New()
	if len(certHashes) > 0 {
		hashesArr := js.Global().Get("Array").New()
		for _, hash := range certHashes {
			hashObj := js.Global().Get("Object").New()
			hashObj.Set("algorithm", "sha-256")
			buf := js.Global().Get("ArrayBuffer").New(len(hash))
			arr := js.Global().Get("Uint8Array").New(buf)
			js.CopyBytesToJS(arr, hash)
			hashObj.Set("value", buf)
			hashesArr.Call("push", hashObj)
		}
		opts.Set("serverCertificateHashes", hashesArr)
	}

	wt := wtClass.New(url, opts)

	// Await transport.ready
	_, err := awaitPromise(wt.Get("ready"))
	if err != nil {
		return nil, fmt.Errorf("webtransport: dial %s: %w", url, err)
	}

	sess := &Session{
		transport: wt,
		incoming:  make(chan portal.Stream, 16),
		closeCh:   make(chan struct{}),
	}

	go sess.acceptLoop()

	return sess, nil
}

// OpenStream creates a new bidirectional stream.
func (s *Session) OpenStream(ctx context.Context) (portal.Stream, error) {
	select {
	case <-s.closeCh:
		return nil, ErrSessionClosed
	default:
	}

	promise := s.transport.Call("createBidirectionalStream")
	bidi, err := awaitPromise(promise)
	if err != nil {
		return nil, fmt.Errorf("webtransport: open stream: %w", err)
	}

	return newStream(bidi), nil
}

// AcceptStream waits for a remote-initiated bidirectional stream.
func (s *Session) AcceptStream(ctx context.Context) (portal.Stream, error) {
	select {
	case <-s.closeCh:
		return nil, ErrSessionClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case stream, ok := <-s.incoming:
		if !ok {
			return nil, ErrSessionClosed
		}
		return stream, nil
	}
}

// Close closes the WebTransport session.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		close(s.closeCh)
		s.transport.Call("close")
		s.releaseFuncs()
	})
	return nil
}

// acceptLoop reads incoming bidirectional streams from the transport.
func (s *Session) acceptLoop() {
	incomingStreams := s.transport.Get("incomingBidirectionalStreams")
	if incomingStreams.IsUndefined() || incomingStreams.IsNull() {
		return
	}
	reader := incomingStreams.Call("getReader")

	for {
		result, err := awaitPromise(reader.Call("read"))
		if err != nil {
			return
		}
		if result.Get("done").Bool() {
			return
		}

		bidi := result.Get("value")
		stream := newStream(bidi)

		select {
		case s.incoming <- stream:
		case <-s.closeCh:
			stream.Close()
			return
		}
	}
}

func (s *Session) addFunc(fn js.Func) {
	s.funcsMu.Lock()
	s.funcs = append(s.funcs, fn)
	s.funcsMu.Unlock()
}

func (s *Session) releaseFuncs() {
	s.funcsMu.Lock()
	for _, fn := range s.funcs {
		fn.Release()
	}
	s.funcs = nil
	s.funcsMu.Unlock()
}

// awaitPromise blocks until a JS Promise resolves or rejects.
func awaitPromise(promise js.Value) (js.Value, error) {
	ch := make(chan js.Value, 1)
	errCh := make(chan error, 1)

	thenFn := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		if len(args) > 0 {
			ch <- args[0]
		} else {
			ch <- js.Undefined()
		}
		return nil
	})

	catchFn := js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		if len(args) > 0 && !args[0].IsUndefined() && !args[0].IsNull() {
			errCh <- fmt.Errorf("%s", args[0].Call("toString").String())
		} else {
			errCh <- errors.New("unknown error")
		}
		return nil
	})

	defer thenFn.Release()
	defer catchFn.Release()

	promise.Call("then", thenFn).Call("catch", catchFn)

	select {
	case val := <-ch:
		return val, nil
	case err := <-errCh:
		return js.Undefined(), err
	}
}
