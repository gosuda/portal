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

	"golang.org/x/sync/errgroup"

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

type ListenerEntry struct {
	RelayURL  string
	LeaseID   string
	Hostnames []string
	Metadata  types.LeaseMetadata
}

// PublicURLs returns the HTTPS URLs exposed by one relay-specific lease.
func (e ListenerEntry) PublicURLs() []string {
	urls := make([]string, 0, len(e.Hostnames))
	for _, host := range e.Hostnames {
		urls = append(urls, "https://"+host)
	}
	return urls
}

func (e ListenerEntry) clone() ListenerEntry {
	e.Hostnames = append([]string(nil), e.Hostnames...)
	return e
}

type Listener struct {
	baseContext func() context.Context
	ctxDone     <-chan struct{}
	cancel      context.CancelFunc
	accepted    chan acceptedConn
	entries     []*listenerLease
	closeOnce   sync.Once
}

type listenerLease struct {
	tlsCloser      io.Closer
	tlsConfig      *tls.Config
	parent         *Listener
	client         *relayClient
	signal         chan struct{}
	info           ListenerEntry
	reverseToken   string
	readyTarget    int
	leaseTTL       time.Duration
	activeSessions int
	mu             sync.Mutex
}

type acceptedConn struct {
	conn  net.Conn
	entry ListenerEntry
}

func (l *Listener) Accept() (net.Conn, error) {
	conn, _, err := l.AcceptEntry()
	return conn, err
}

// AcceptEntry returns the next accepted connection plus relay-specific lease
// metadata for callers that need to distinguish which relay claimed it.
func (l *Listener) AcceptEntry() (net.Conn, ListenerEntry, error) {
	select {
	case <-l.ctxDone:
		return nil, ListenerEntry{}, net.ErrClosed
	case accepted := <-l.accepted:
		if accepted.conn == nil {
			return nil, ListenerEntry{}, net.ErrClosed
		}
		return accepted.conn, accepted.entry.clone(), nil
	}
}

func (l *Listener) Close() error {
	var closeErr error
	l.closeOnce.Do(func() {
		l.cancel()
		closeErr = closeListenerEntries(l.entries)
	})
	return closeErr
}

func (l *Listener) Addr() net.Addr {
	if entry, ok := l.singleEntry(); ok {
		return listenerAddr("portal:" + entry.LeaseID)
	}
	return listenerAddr("portal:multi")
}

// Entries returns relay-specific lease details for advanced multi-relay callers.
func (l *Listener) Entries() []ListenerEntry {
	entries := make([]ListenerEntry, 0, len(l.entries))
	for _, entry := range l.entries {
		entries = append(entries, entry.info.clone())
	}
	return entries
}

func (l *Listener) singleEntry() (ListenerEntry, bool) {
	if len(l.entries) != 1 {
		return ListenerEntry{}, false
	}
	return l.entries[0].info.clone(), true
}

// PublicURLs returns all public HTTPS URLs exposed by the listener.
func (l *Listener) PublicURLs() []string {
	var urls []string
	for _, entry := range l.entries {
		urls = append(urls, entry.info.PublicURLs()...)
	}
	return urls
}

func closeListenerEntries(entries []*listenerLease) error {
	if len(entries) == 0 {
		return nil
	}

	var closeErr error
	var mu sync.Mutex
	var group errgroup.Group
	group.SetLimit(min(len(entries), 4))

	for _, entry := range entries {
		if entry == nil {
			continue
		}
		group.Go(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if err := entry.close(ctx); err != nil {
				mu.Lock()
				closeErr = errors.Join(closeErr, err)
				mu.Unlock()
			}
			return nil
		})
	}

	_ = group.Wait()

	return closeErr
}

func (l *listenerLease) runSupervisor() {
	for {
		select {
		case <-l.parent.ctxDone:
			return
		case <-l.signal:
		}

		for l.reserveSessionSlot() {
			go l.runSession()
		}
	}
}

func (l *listenerLease) runRenewLoop() {
	interval := l.leaseTTL / 2
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if defaultRenewBefore > 0 && l.leaseTTL > defaultRenewBefore {
		interval = l.leaseTTL - defaultRenewBefore
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-l.parent.ctxDone:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(l.context(), 10*time.Second)
			_ = l.client.renewLease(ctx, l.info.LeaseID, l.reverseToken, l.leaseTTL)
			cancel()
		}
	}
}

func (l *listenerLease) runSession() {
	defer l.releaseSessionSlot()

	sessionCtx := l.context()
	conn, err := l.client.openReverseSession(sessionCtx, l.info.LeaseID, l.reverseToken)
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

func (l *listenerLease) awaitActivation(conn net.Conn) error {
	var marker [1]byte
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * defaultHandshakeTimeout))
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

func (l *listenerLease) activate(conn net.Conn) error {
	tlsConn := tls.Server(conn, l.tlsConfig.Clone())
	handshakeCtx, cancel := context.WithTimeout(l.context(), defaultHandshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		return err
	}

	select {
	case <-l.parent.ctxDone:
		_ = tlsConn.Close()
		return l.context().Err()
	case l.parent.accepted <- acceptedConn{conn: tlsConn, entry: l.info.clone()}:
		return nil
	}
}

func (l *listenerLease) reserveSessionSlot() bool {
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

func (l *listenerLease) releaseSessionSlot() {
	l.mu.Lock()
	l.activeSessions--
	l.mu.Unlock()
	l.notify()
}

func (l *listenerLease) notify() {
	select {
	case l.signal <- struct{}{}:
	default:
	}
}

func (l *listenerLease) close(ctx context.Context) error {
	if l == nil {
		return nil
	}

	var closeErr error
	if l.client != nil {
		if err := l.client.unregisterLease(ctx, l.info.LeaseID, l.reverseToken); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	if l.tlsCloser != nil {
		closeErr = errors.Join(closeErr, l.tlsCloser.Close())
	}
	return closeErr
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

func (l *listenerLease) context() context.Context {
	if l.parent != nil && l.parent.baseContext != nil {
		if ctx := l.parent.baseContext(); ctx != nil {
			return ctx
		}
	}
	return context.Background()
}

func (l *listenerLease) isClosed() bool {
	if l.parent == nil || l.parent.ctxDone == nil {
		return false
	}
	select {
	case <-l.parent.ctxDone:
		return true
	default:
		return false
	}
}
