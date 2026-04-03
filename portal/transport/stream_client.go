package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/gosuda/portal/v2/types"
)

type ClientStream struct {
	accepted         chan net.Conn
	activeSessions   int
	handshakeTimeout time.Duration
	mu               sync.Mutex
}

func NewClientStream(readyTarget int, handshakeTimeout time.Duration) *ClientStream {
	return &ClientStream{
		accepted:         make(chan net.Conn, max(readyTarget*2, 1)),
		handshakeTimeout: handshakeTimeout,
	}
}

func (s *ClientStream) Accept(done <-chan struct{}) (net.Conn, error) {
	if s == nil {
		return nil, net.ErrClosed
	}
	select {
	case <-done:
		return nil, net.ErrClosed
	case conn := <-s.accepted:
		if conn == nil {
			return nil, net.ErrClosed
		}
		return conn, nil
	}
}

func (s *ClientStream) RunLoop(
	ctx context.Context,
	open func(context.Context) (net.Conn, error),
	currentTLSConfig func() *tls.Config,
	onReady func(),
	onInactive func(),
	retry func(context.Context, string, error, int) bool,
) {
	var retries int

	for {
		claimed, err := s.runSession(ctx, open, currentTLSConfig, onReady)
		switch {
		case err == nil:
			retries = 0
		case errors.Is(err, context.Canceled), errors.Is(err, net.ErrClosed):
			return
		case claimed:
			retries = 0
		default:
			retries++
			if s.ActiveSessions() == 0 && onInactive != nil {
				onInactive()
			}
			if retry == nil || !retry(ctx, "reverse session connect", err, retries) {
				return
			}
		}
	}
}

func (s *ClientStream) ActiveSessions() int {
	if s == nil {
		return 0
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeSessions
}

func (s *ClientStream) Drain() {
	if s == nil {
		return
	}
	for {
		select {
		case conn := <-s.accepted:
			if conn != nil {
				_ = conn.Close()
			}
		default:
			return
		}
	}
}

func (s *ClientStream) runSession(
	ctx context.Context,
	open func(context.Context) (net.Conn, error),
	currentTLSConfig func() *tls.Config,
	onReady func(),
) (bool, error) {
	conn, err := open(ctx)
	if err != nil {
		return false, err
	}
	s.sessionOpened()
	defer s.sessionClosed()

	var marker [1]byte
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * s.handshakeTimeout))
		if _, err := io.ReadFull(conn, marker[:]); err != nil {
			_ = conn.Close()
			return false, err
		}
		_ = conn.SetReadDeadline(time.Time{})

		switch marker[0] {
		case types.MarkerKeepalive:
			continue
		case types.MarkerTLSStart:
			if err := s.activate(ctx, conn, currentTLSConfig); err != nil {
				_ = conn.Close()
				return true, err
			}
			if onReady != nil {
				onReady()
			}
			return true, nil
		case types.MarkerRawStart:
			if err := s.activateRaw(ctx, conn); err != nil {
				_ = conn.Close()
				return true, err
			}
			if onReady != nil {
				onReady()
			}
			return true, nil
		default:
			_ = conn.Close()
			return false, fmt.Errorf("unexpected reverse marker: 0x%02x", marker[0])
		}
	}
}

func (s *ClientStream) activate(ctx context.Context, conn net.Conn, currentTLSConfig func() *tls.Config) error {
	var tlsCfg *tls.Config
	if currentTLSConfig != nil {
		tlsCfg = currentTLSConfig()
	}
	if tlsCfg == nil {
		return errors.New("tls config is unavailable")
	}

	tlsConn := tls.Server(conn, tlsCfg)
	handshakeCtx, cancel := context.WithTimeout(ctx, s.handshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		_ = tlsConn.Close()
		return ctx.Err()
	case s.accepted <- tlsConn:
		return nil
	}
}

func (s *ClientStream) activateRaw(ctx context.Context, conn net.Conn) error {
	select {
	case <-ctx.Done():
		_ = conn.Close()
		return ctx.Err()
	case s.accepted <- conn:
		return nil
	}
}

func (s *ClientStream) sessionOpened() {
	if s == nil {
		return
	}

	s.mu.Lock()
	s.activeSessions++
	s.mu.Unlock()
}

func (s *ClientStream) sessionClosed() {
	if s == nil {
		return
	}

	s.mu.Lock()
	if s.activeSessions > 0 {
		s.activeSessions--
	}
	s.mu.Unlock()
}
