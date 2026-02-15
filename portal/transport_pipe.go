package portal

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

var (
	// ErrPipeSessionClosed is returned when operating on a closed PipeSession.
	ErrPipeSessionClosed = errors.New("portal: pipe session closed")
	// ErrPipeStreamClosed is returned when operating on a closed bufferedPipeStream.
	ErrPipeStreamClosed = errors.New("portal: pipe stream closed")
)

// PipeSession is an in-memory Session implementation for testing.
// Streams use buffered channels to avoid blocking, unlike net.Pipe which is synchronous.
type PipeSession struct {
	mu       sync.Mutex
	peer     *PipeSession
	incoming chan Stream
	streams  []*bufferedPipeStream // Track created streams
	closed   bool
}

// bufferedPipeStream implements Stream using buffered channels.
// Unlike net.Pipe(), writes don't block waiting for reads (up to buffer limit).
type bufferedPipeStream struct {
	readCh        <-chan []byte
	writeCh       chan<- []byte
	closeOnce     sync.Once
	closeCh       chan struct{}
	readBuf       []byte // Partial read buffer
	mu            sync.Mutex
	closed        bool
	readDeadline  time.Time
	writeDeadline time.Time
}

// Ensure bufferedPipeStream implements Stream.
var _ Stream = (*bufferedPipeStream)(nil)

// NewPipeSessionPair creates a connected pair of PipeSessions.
// Streams opened on client will be accepted on server, and vice versa.
func NewPipeSessionPair() (client *PipeSession, server *PipeSession) {
	client = &PipeSession{incoming: make(chan Stream, 8)}
	server = &PipeSession{incoming: make(chan Stream, 8)}
	client.peer = server
	server.peer = client
	return client, server
}

// OpenStream creates a new bidirectional stream.
// The remote peer's AcceptStream will receive the other end.
func (s *PipeSession) OpenStream(ctx context.Context) (Stream, error) {
	// Check context first
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrPipeSessionClosed
	}
	peer := s.peer
	s.mu.Unlock()

	if peer == nil {
		return nil, errors.New("no peer")
	}

	// Create bidirectional buffered pipes
	// Each direction has a 16KB buffer (about 16 messages of 1KB each)
	ch1 := make(chan []byte, 16)
	ch2 := make(chan []byte, 16)
	closeCh1 := make(chan struct{})
	closeCh2 := make(chan struct{})

	local := &bufferedPipeStream{
		readCh:  ch1,
		writeCh: ch2,
		closeCh: closeCh1,
	}

	remote := &bufferedPipeStream{
		readCh:  ch2,
		writeCh: ch1,
		closeCh: closeCh2,
	}

	// Track the local stream so we can close it when session closes
	s.mu.Lock()
	s.streams = append(s.streams, local)
	s.mu.Unlock()

	// Send remote end to peer's incoming channel
	select {
	case peer.incoming <- remote:
		return local, nil
	case <-ctx.Done():
		local.Close()
		remote.Close()
		return nil, ctx.Err()
	}
}

// Read implements io.Reader for bufferedPipeStream.
func (s *bufferedPipeStream) Read(p []byte) (n int, err error) {
	s.mu.Lock()
	deadline := s.readDeadline
	// If we have buffered data from a previous read, use it first
	if len(s.readBuf) > 0 {
		n = copy(p, s.readBuf)
		s.readBuf = s.readBuf[n:]
		s.mu.Unlock()
		return n, nil
	}
	s.mu.Unlock()

	// Set up deadline timer if needed
	var timeoutCh <-chan time.Time
	if !deadline.IsZero() {
		timeout := time.Until(deadline)
		if timeout <= 0 {
			return 0, ErrPipeStreamClosed
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	// Wait for new data, close, or timeout
	select {
	case <-s.closeCh:
		return 0, io.EOF
	case data, ok := <-s.readCh:
		if !ok {
			return 0, io.EOF
		}
		n = copy(p, data)
		if n < len(data) {
			// Store the rest for next read
			s.mu.Lock()
			s.readBuf = data[n:]
			s.mu.Unlock()
		}
		return n, nil
	case <-timeoutCh:
		return 0, ErrPipeStreamClosed
	}
}

// Write implements io.Writer for bufferedPipeStream.
func (s *bufferedPipeStream) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, ErrPipeStreamClosed
	}
	deadline := s.writeDeadline
	s.mu.Unlock()

	// Make a copy since caller may reuse the buffer
	data := make([]byte, len(p))
	copy(data, p)

	// Set up deadline timer if needed
	var timeoutCh <-chan time.Time
	if !deadline.IsZero() {
		timeout := time.Until(deadline)
		if timeout <= 0 {
			return 0, ErrPipeStreamClosed
		}
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	select {
	case <-s.closeCh:
		return 0, ErrPipeStreamClosed
	case s.writeCh <- data:
		return len(p), nil
	case <-timeoutCh:
		return 0, ErrPipeStreamClosed
	}
}

// Close implements io.Closer for bufferedPipeStream.
func (s *bufferedPipeStream) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.closeCh)
	})
	return nil
}

// SetDeadline implements Stream.SetDeadline.
func (s *bufferedPipeStream) SetDeadline(t time.Time) error {
	s.mu.Lock()
	s.readDeadline = t
	s.writeDeadline = t
	s.mu.Unlock()
	return nil
}

// SetReadDeadline implements Stream.SetReadDeadline.
func (s *bufferedPipeStream) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	s.readDeadline = t
	s.mu.Unlock()
	return nil
}

// SetWriteDeadline implements Stream.SetWriteDeadline.
func (s *bufferedPipeStream) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	s.writeDeadline = t
	s.mu.Unlock()
	return nil
}

// AcceptStream waits for a new stream from the remote peer.
func (s *PipeSession) AcceptStream(ctx context.Context) (Stream, error) {
	s.mu.Lock()
	incoming := s.incoming
	closed := s.closed
	s.mu.Unlock()

	if closed {
		return nil, ErrPipeSessionClosed
	}

	select {
	case stream, ok := <-incoming:
		if !ok {
			return nil, ErrPipeSessionClosed
		}
		// Track the accepted stream
		if bps, ok := stream.(*bufferedPipeStream); ok {
			s.mu.Lock()
			s.streams = append(s.streams, bps)
			s.mu.Unlock()
		}
		return stream, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close terminates the session and releases resources.
func (s *PipeSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil // Idempotent
	}

	s.closed = true

	// Close all tracked streams
	for _, stream := range s.streams {
		if stream != nil {
			stream.Close()
		}
	}
	s.streams = nil

	close(s.incoming)

	// Drain and close any pending streams in the incoming channel
	for stream := range s.incoming {
		if stream != nil {
			stream.Close()
		}
	}

	return nil
}

// Ensure PipeSession implements Session.
var _ Session = (*PipeSession)(nil)
