package portal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/types"
)

var (
	errLeaseDropped = errors.New("lease dropped")
	errLeaseStopped = errors.New("lease stopped")
	errBrokerFull   = errors.New("broker ready queue full")
)

type brokerState int

const (
	brokerStateActive brokerState = iota
	brokerStateDropped
	brokerStateStopped
)

type leaseBroker struct {
	notify       chan struct{}
	leaseID      string
	ready        []*reverseSession
	idleInterval time.Duration
	readyLimit   int
	state        brokerState
	mu           sync.Mutex
}

func newLeaseBroker(leaseID string, idleInterval time.Duration, readyLimit int) *leaseBroker {
	return &leaseBroker{
		leaseID:      leaseID,
		idleInterval: idleInterval,
		readyLimit:   readyLimit,
		notify:       make(chan struct{}, 1),
	}
}

func (b *leaseBroker) Offer(session *reverseSession) error {
	if session == nil {
		return errors.New("reverse session is required")
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case brokerStateDropped:
		return errLeaseDropped
	case brokerStateStopped:
		return errLeaseStopped
	}

	if b.readyLimit > 0 && len(b.ready) >= b.readyLimit {
		return errBrokerFull
	}

	session.StartIdle()
	b.ready = append(b.ready, session)
	b.signalLocked()
	go b.watchSession(session)
	return nil
}

func (b *leaseBroker) Claim(ctx context.Context) (*reverseSession, error) {
	for {
		b.mu.Lock()
		switch b.state {
		case brokerStateDropped:
			b.mu.Unlock()
			return nil, errLeaseDropped
		case brokerStateStopped:
			b.mu.Unlock()
			return nil, errLeaseStopped
		}

		if len(b.ready) > 0 {
			session := b.ready[0]
			b.ready = b.ready[1:]
			b.mu.Unlock()

			if session.IsClosed() {
				continue
			}
			if err := session.Activate(); err != nil {
				_ = session.Close()
				continue
			}
			return session, nil
		}
		b.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-b.notify:
		}
	}
}

func (b *leaseBroker) Drop() {
	b.transition(brokerStateDropped)
}

func (b *leaseBroker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == brokerStateDropped {
		b.state = brokerStateActive
		b.signalLocked()
	}
}

func (b *leaseBroker) Stop() {
	b.transition(brokerStateStopped)
}

func (b *leaseBroker) ReadyCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.ready)
}

func (b *leaseBroker) transition(state brokerState) {
	b.mu.Lock()
	sessions := b.ready
	b.ready = nil
	b.state = state
	b.signalLocked()
	b.mu.Unlock()

	for _, session := range sessions {
		_ = session.Close()
	}
}

func (b *leaseBroker) watchSession(session *reverseSession) {
	<-session.Done()
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.ready {
		if b.ready[i] == session {
			b.ready = append(b.ready[:i], b.ready[i+1:]...)
			break
		}
	}
	log.Info().
		Str("component", "relay-server").
		Str("lease_id", b.leaseID).
		Str("remote_addr", session.RemoteAddr()).
		Int("ready", len(b.ready)).
		Msg("sdk reverse disconnected")
	b.signalLocked()
}

func (b *leaseBroker) signalLocked() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

type reverseSessionState int

const (
	reverseSessionAdmitted reverseSessionState = iota
	reverseSessionIdle
	reverseSessionClaimed
	reverseSessionClosed
)

type reverseSession struct {
	conn          net.Conn
	keepaliveStop chan struct{}
	keepaliveDone chan struct{}
	done          chan struct{}
	idleInterval  time.Duration
	state         reverseSessionState
	closeOnce     sync.Once
	mu            sync.Mutex
}

func newReverseSession(conn net.Conn, idleInterval time.Duration) *reverseSession {
	return &reverseSession{
		conn:         conn,
		idleInterval: idleInterval,
		state:        reverseSessionAdmitted,
		done:         make(chan struct{}),
	}
}

func (s *reverseSession) Conn() net.Conn {
	return s.conn
}

func (s *reverseSession) Done() <-chan struct{} {
	return s.done
}

func (s *reverseSession) RemoteAddr() string {
	if s == nil || s.conn == nil || s.conn.RemoteAddr() == nil {
		return ""
	}
	return s.conn.RemoteAddr().String()
}

func (s *reverseSession) IsClosed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *reverseSession) StartIdle() {
	s.mu.Lock()
	if s.state != reverseSessionAdmitted {
		s.mu.Unlock()
		return
	}
	s.state = reverseSessionIdle
	stop := make(chan struct{})
	done := make(chan struct{})
	s.keepaliveStop = stop
	s.keepaliveDone = done
	s.mu.Unlock()

	go s.runKeepalive(stop, done)
}

func (s *reverseSession) Activate() error {
	s.mu.Lock()
	if s.state != reverseSessionIdle {
		state := s.state
		s.mu.Unlock()
		return fmt.Errorf("session not idle: %d", state)
	}
	stop := s.keepaliveStop
	done := s.keepaliveDone
	s.keepaliveStop = nil
	s.keepaliveDone = nil
	s.state = reverseSessionClaimed
	s.mu.Unlock()

	if stop != nil {
		close(stop)
	}
	if done != nil {
		<-done
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == reverseSessionClosed {
		return net.ErrClosed
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(defaultSessionWriteLimit))
	_, err := s.conn.Write([]byte{types.MarkerTLSStart})
	_ = s.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		_ = s.Close()
	}
	return err
}

func (s *reverseSession) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.mu.Lock()
		stop := s.keepaliveStop
		done := s.keepaliveDone
		s.keepaliveStop = nil
		s.keepaliveDone = nil
		s.state = reverseSessionClosed
		conn := s.conn
		s.mu.Unlock()

		if stop != nil {
			close(stop)
		}
		if done != nil {
			<-done
		}

		err = conn.Close()
		close(s.done)
	})
	return err
}

func (s *reverseSession) runKeepalive(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)

	ticker := time.NewTicker(s.idleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-s.done:
			return
		case <-ticker.C:
		}

		s.mu.Lock()
		if s.state != reverseSessionIdle {
			s.mu.Unlock()
			return
		}
		_ = s.conn.SetWriteDeadline(time.Now().Add(defaultSessionWriteLimit))
		_, err := s.conn.Write([]byte{types.MarkerKeepalive})
		_ = s.conn.SetWriteDeadline(time.Time{})
		s.mu.Unlock()
		if err != nil {
			_ = s.Close()
			return
		}
	}
}
