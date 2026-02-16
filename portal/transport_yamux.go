package portal

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/hashicorp/yamux"
)

// YamuxSession adapts a *yamux.Session to the Session interface.
// This is a transitional adapter; it will be removed when WebTransport replaces yamux.
type YamuxSession struct {
	sess *yamux.Session
	conn io.Closer // underlying transport, closed after session
}

// Ensure YamuxSession implements Session.
var _ Session = (*YamuxSession)(nil)

// NewYamuxClientSession creates a client-side yamux Session from an io.ReadWriteCloser.
func NewYamuxClientSession(conn io.ReadWriteCloser) (Session, error) {
	cfg := defaultYamuxConfig()
	sess, err := yamux.Client(conn, cfg)
	if err != nil {
		return nil, err
	}
	return &YamuxSession{sess: sess, conn: conn}, nil
}

// NewYamuxServerSession creates a server-side yamux Session from an io.ReadWriteCloser.
func NewYamuxServerSession(conn io.ReadWriteCloser) (Session, error) {
	cfg := defaultYamuxConfig()
	sess, err := yamux.Server(conn, cfg)
	if err != nil {
		return nil, err
	}
	return &YamuxSession{sess: sess, conn: conn}, nil
}

func defaultYamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.Logger = nil
	cfg.MaxStreamWindowSize = 16 * 1024 * 1024 // 16MB for high-BDP scenarios
	cfg.StreamOpenTimeout = 75 * time.Second
	cfg.StreamCloseTimeout = 5 * time.Minute
	return cfg
}

// OpenStream creates a new bidirectional stream.
// The context is checked before the blocking call; yamux does not natively support context cancellation.
func (s *YamuxSession) OpenStream(ctx context.Context) (Stream, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return s.sess.OpenStream()
}

// AcceptStream waits for a new stream from the remote peer.
// The context is checked before the blocking call; yamux does not natively support context cancellation.
func (s *YamuxSession) AcceptStream(ctx context.Context) (Stream, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return s.sess.AcceptStream()
}

// Close terminates the yamux session and underlying transport.
func (s *YamuxSession) Close() error {
	err1 := s.sess.Close()
	var err2 error
	if s.conn != nil {
		err2 = s.conn.Close()
	}
	return errors.Join(err1, err2)
}

// Ping exposes yamux's built-in ping for health checking.
func (s *YamuxSession) Ping() (time.Duration, error) {
	return s.sess.Ping()
}
