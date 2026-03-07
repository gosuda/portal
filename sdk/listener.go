package sdk

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

type ListenRequest struct {
	Name         string
	ReverseToken string
	Hostnames    []string
	Metadata     types.LeaseMetadata
	ReadyTarget  int
	LeaseTTL     time.Duration
}

type Listener struct {
	tlsCloser    io.Closer
	tlsConfig    *tls.Config
	baseContext  func() context.Context
	ctxDone      <-chan struct{}
	cancel       context.CancelFunc
	client       *Client
	signal       chan struct{}
	accepted     chan net.Conn
	leaseID      string
	reverseToken string
	hostnames    []string
	metadata     types.LeaseMetadata
	readyTarget  int
	leaseTTL     time.Duration

	activeSessions int
	closeOnce      sync.Once
	mu             sync.Mutex
}

func (l *Listener) Accept() (net.Conn, error) {
	select {
	case <-l.ctxDone:
		return nil, net.ErrClosed
	case conn := <-l.accepted:
		if conn == nil {
			return nil, net.ErrClosed
		}
		return conn, nil
	}
}

func (l *Listener) Close() error {
	var closeErr error
	l.closeOnce.Do(func() {
		l.cancel()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := l.client.unregisterLease(ctx, l.leaseID, l.reverseToken); err != nil {
			closeErr = err
		}
		if l.tlsCloser != nil {
			closeErr = errors.Join(closeErr, l.tlsCloser.Close())
		}
	})
	return closeErr
}

func (l *Listener) Addr() net.Addr {
	return listenerAddr("portal:" + l.leaseID)
}

func (l *Listener) LeaseID() string {
	return l.leaseID
}

func (l *Listener) Hostnames() []string {
	return append([]string(nil), l.hostnames...)
}

func (l *Listener) Metadata() types.LeaseMetadata {
	return cloneLeaseMetadata(l.metadata)
}

func (l *Listener) PublicURLs() []string {
	urls := make([]string, 0, len(l.hostnames))
	for _, host := range l.hostnames {
		urls = append(urls, "https://"+host)
	}
	return urls
}

func (l *Listener) runSupervisor() {
	for {
		select {
		case <-l.ctxDone:
			return
		case <-l.signal:
		}

		for l.reserveSessionSlot() {
			go l.runSession()
		}
	}
}

func (l *Listener) runRenewLoop() {
	interval := l.leaseTTL / 2
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if l.client.renewBefore > 0 && l.leaseTTL > l.client.renewBefore {
		interval = l.leaseTTL - l.client.renewBefore
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-l.ctxDone:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(l.context(), 10*time.Second)
			_ = l.client.renewLease(ctx, l.leaseID, l.reverseToken, l.leaseTTL)
			cancel()
		}
	}
}

func (l *Listener) runSession() {
	defer l.releaseSessionSlot()

	sessionCtx := l.context()
	conn, err := l.client.openReverseSession(sessionCtx, l.leaseID, l.reverseToken)
	if err != nil {
		sleepOrDone(sessionCtx, time.Second)
		return
	}

	if err := l.awaitActivation(conn); err != nil {
		_ = conn.Close()
		if !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
			sleepOrDone(sessionCtx, time.Second)
		}
	}
}

func (l *Listener) awaitActivation(conn net.Conn) error {
	var marker [1]byte
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * l.client.handshakeTimeout))
		if _, err := io.ReadFull(conn, marker[:]); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Time{})

		switch marker[0] {
		case types.MarkerKeepalive:
			continue
		case types.MarkerTLSStart:
			return l.activate(conn)
		default:
			return fmt.Errorf("unexpected reverse marker: 0x%02x", marker[0])
		}
	}
}

func (l *Listener) activate(conn net.Conn) error {
	tlsConn := tls.Server(conn, l.tlsConfig.Clone())
	handshakeCtx, cancel := context.WithTimeout(l.context(), l.client.handshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		return err
	}

	select {
	case <-l.ctxDone:
		_ = tlsConn.Close()
		return l.context().Err()
	case l.accepted <- tlsConn:
		return nil
	}
}

func (l *Listener) reserveSessionSlot() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.isClosed() {
		return false
	}
	if l.activeSessions >= l.readyTarget {
		return false
	}
	l.activeSessions++
	return true
}

func (l *Listener) releaseSessionSlot() {
	l.mu.Lock()
	l.activeSessions--
	l.mu.Unlock()
	l.notify()
}

func (l *Listener) notify() {
	select {
	case l.signal <- struct{}{}:
	default:
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

type listenerAddr string

func (a listenerAddr) Network() string { return "portal" }
func (a listenerAddr) String() string  { return string(a) }

func (l *Listener) context() context.Context {
	if l.baseContext != nil {
		if ctx := l.baseContext(); ctx != nil {
			return ctx
		}
	}
	return context.Background()
}

func (l *Listener) isClosed() bool {
	if l.ctxDone == nil {
		return false
	}
	select {
	case <-l.ctxDone:
		return true
	default:
		return false
	}
}

func cloneLeaseMetadata(metadata types.LeaseMetadata) types.LeaseMetadata {
	metadata.Tags = append([]string(nil), metadata.Tags...)
	return metadata
}
