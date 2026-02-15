package portal

import (
	"context"
	"io"
	"time"
)

// Session abstracts a multiplexed transport connection (WebTransport, yamux, etc.).
// A Session manages multiple independent bidirectional streams over a single underlying
// connection, providing concurrent communication channels.
type Session interface {
	// OpenStream creates a new bidirectional stream within the session.
	// Returns an error if the session is closed or the underlying transport fails.
	OpenStream(ctx context.Context) (Stream, error)

	// AcceptStream blocks until a new stream is initiated by the remote peer,
	// or the context is canceled. Returns an error if the session is closed.
	AcceptStream(ctx context.Context) (Stream, error)

	// Close terminates the session and all active streams.
	// Subsequent calls to OpenStream or AcceptStream will fail.
	Close() error
}

// Stream abstracts a single bidirectional stream within a Session.
// Streams provide independent flow control and ordering guarantees
// separate from other streams in the same session.
type Stream interface {
	io.ReadWriteCloser

	// SetDeadline sets the read and write deadlines for the stream.
	// A zero value disables the deadline.
	SetDeadline(t time.Time) error

	// SetReadDeadline sets the deadline for future Read calls.
	// A zero value disables the deadline.
	SetReadDeadline(t time.Time) error

	// SetWriteDeadline sets the deadline for future Write calls.
	// A zero value disables the deadline.
	SetWriteDeadline(t time.Time) error
}
