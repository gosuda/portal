package portal

import (
	"context"
	"time"

	"github.com/quic-go/webtransport-go"
)

// WTSession wraps a webtransport.Session to implement the portal.Session interface.
type WTSession struct {
	sess *webtransport.Session
}

// NewWTSession creates a new WTSession wrapping the provided webtransport.Session.
func NewWTSession(sess *webtransport.Session) *WTSession {
	return &WTSession{sess: sess}
}

// OpenStream creates a new bidirectional stream within the session.
func (w *WTSession) OpenStream(ctx context.Context) (Stream, error) {
	stream, err := w.sess.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return &WTStream{stream: stream}, nil
}

// AcceptStream blocks until a new stream is initiated by the remote peer.
func (w *WTSession) AcceptStream(ctx context.Context) (Stream, error) {
	stream, err := w.sess.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return &WTStream{stream: stream}, nil
}

// Close terminates the session and all active streams.
func (w *WTSession) Close() error {
	return w.sess.CloseWithError(0, "")
}

// Verify interface compliance at compile time.
var _ Session = (*WTSession)(nil)

// WTStream wraps a webtransport.Stream to implement the portal.Stream interface.
type WTStream struct {
	stream *webtransport.Stream
}

// Read reads data from the stream.
func (w *WTStream) Read(p []byte) (n int, err error) {
	return w.stream.Read(p)
}

// Write writes data to the stream.
func (w *WTStream) Write(p []byte) (n int, err error) {
	return w.stream.Write(p)
}

// Close closes both the send and receive directions of the stream.
// This is critical because webtransport.Stream.Close() only closes the send direction,
// so we must also call CancelRead(0) to fully close the bidirectional stream.
func (w *WTStream) Close() error {
	w.stream.CancelRead(webtransport.StreamErrorCode(0))
	return w.stream.Close()
}

// SetDeadline sets the read and write deadlines for the stream.
func (w *WTStream) SetDeadline(t time.Time) error {
	return w.stream.SetDeadline(t)
}

// SetReadDeadline sets the deadline for future Read calls.
func (w *WTStream) SetReadDeadline(t time.Time) error {
	return w.stream.SetReadDeadline(t)
}

// SetWriteDeadline sets the deadline for future Write calls.
func (w *WTStream) SetWriteDeadline(t time.Time) error {
	return w.stream.SetWriteDeadline(t)
}

// Verify interface compliance at compile time.
var _ Stream = (*WTStream)(nil)
