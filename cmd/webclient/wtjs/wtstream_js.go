package wtjs

import (
	"io"
	"sync"
	"syscall/js"
	"time"

	"gosuda.org/portal/portal"
)

// Stream wraps a browser WebTransportBidirectionalStream and implements portal.Stream.
type Stream struct {
	reader js.Value // ReadableStreamDefaultReader
	writer js.Value // WritableStreamDefaultWriter

	readBuf []byte
	readMu  sync.Mutex
	writeMu sync.Mutex

	closeCh   chan struct{}
	closeOnce sync.Once
}

var _ portal.Stream = (*Stream)(nil)

// newStream wraps a JS WebTransportBidirectionalStream.
func newStream(bidi js.Value) *Stream {
	readable := bidi.Get("readable").Call("getReader")
	writable := bidi.Get("writable").Call("getWriter")
	return &Stream{
		reader:  readable,
		writer:  writable,
		closeCh: make(chan struct{}),
	}
}

// Read reads data from the stream.
// Blocks until data is available or the stream ends.
func (s *Stream) Read(p []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()

	select {
	case <-s.closeCh:
		return 0, io.ErrClosedPipe
	default:
	}

	// Return buffered data from a previous read.
	if len(s.readBuf) > 0 {
		n := copy(p, s.readBuf)
		s.readBuf = s.readBuf[n:]
		return n, nil
	}

	// Await reader.read()
	result, err := awaitPromise(s.reader.Call("read"))
	if err != nil {
		return 0, err
	}

	if result.Get("done").Bool() {
		return 0, io.EOF
	}

	value := result.Get("value")
	length := value.Get("byteLength").Int()
	data := make([]byte, length)
	uint8View := js.Global().Get("Uint8Array").New(value.Get("buffer"), value.Get("byteOffset"), length)
	js.CopyBytesToGo(data, uint8View)

	n := copy(p, data)
	if n < len(data) {
		s.readBuf = data[n:]
	}
	return n, nil
}

// Write writes data to the stream.
func (s *Stream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	select {
	case <-s.closeCh:
		return 0, io.ErrClosedPipe
	default:
	}

	arr := js.Global().Get("Uint8Array").New(len(p))
	js.CopyBytesToJS(arr, p)

	_, err := awaitPromise(s.writer.Call("write", arr))
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close closes both read and write sides of the stream.
func (s *Stream) Close() error {
	s.closeOnce.Do(func() {
		close(s.closeCh)
		s.reader.Call("cancel")
		// writer.close() returns a Promise; fire and forget.
		s.writer.Call("close")
	})
	return nil
}

// SetDeadline is a no-op in the browser (WebTransport streams do not support deadlines).
func (s *Stream) SetDeadline(t time.Time) error {
	return nil
}

// SetReadDeadline is a no-op in the browser.
func (s *Stream) SetReadDeadline(t time.Time) error {
	return nil
}

// SetWriteDeadline is a no-op in the browser.
func (s *Stream) SetWriteDeadline(t time.Time) error {
	return nil
}
