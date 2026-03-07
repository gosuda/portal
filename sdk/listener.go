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
	e.Metadata = cloneLeaseMetadata(e.Metadata)
	return e
}

type Listener struct {
	ctx       context.Context
	cancel    context.CancelFunc
	accepted  chan acceptedConn
	entries   []*listenerLease
	workers   errgroup.Group
	closeOnce sync.Once

	mu          sync.RWMutex
	activeCount int
	terminalErr error
}

type listenerLease struct {
	parent       *Listener
	client       *relayClient
	reverseToken string
	readyTarget  int
	leaseTTL     time.Duration

	mu          sync.RWMutex
	info        ListenerEntry
	tlsConfig   *tls.Config
	tlsCloser   io.Closer
	active      bool
	terminalErr error
}

type acceptedConn struct {
	conn  net.Conn
	entry ListenerEntry
}

type sessionSnapshot struct {
	info        ListenerEntry
	tlsConfig   *tls.Config
	active      bool
	terminalErr error
}

func newListener(ctx context.Context) *Listener {
	if ctx == nil {
		ctx = context.Background()
	}

	listenerCtx, cancel := context.WithCancel(ctx)
	return &Listener{
		ctx:    listenerCtx,
		cancel: cancel,
	}
}

func (l *Listener) Accept() (net.Conn, error) {
	conn, _, err := l.AcceptEntry()
	return conn, err
}

// AcceptEntry returns the next accepted connection plus relay-specific lease
// metadata for callers that need to distinguish which relay claimed it.
func (l *Listener) AcceptEntry() (net.Conn, ListenerEntry, error) {
	for {
		select {
		case <-l.ctx.Done():
			return nil, ListenerEntry{}, l.closeError()
		case accepted := <-l.accepted:
			if accepted.conn == nil {
				return nil, ListenerEntry{}, l.closeError()
			}
			return accepted.conn, accepted.entry.clone(), nil
		}
	}
}

func (l *Listener) Close() error {
	var closeErr error
	l.closeOnce.Do(func() {
		l.cancel()
		closeErr = errors.Join(l.workers.Wait(), closeListenerEntries(l.entries))
		l.drainAccepted()
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
		info, ok := entry.snapshotInfo()
		if !ok {
			continue
		}
		entries = append(entries, info)
	}
	return entries
}

func (l *Listener) singleEntry() (ListenerEntry, bool) {
	entries := l.Entries()
	if len(entries) != 1 {
		return ListenerEntry{}, false
	}
	return entries[0], true
}

// PublicURLs returns all public HTTPS URLs exposed by the listener.
func (l *Listener) PublicURLs() []string {
	var urls []string
	for _, entry := range l.Entries() {
		urls = append(urls, entry.PublicURLs()...)
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

func (l *listenerLease) start() {
	if l == nil || l.parent == nil {
		return
	}

	l.parent.workers.Go(func() error {
		l.runRenewLoop()
		return nil
	})
	for i := 0; i < l.readyTarget; i++ {
		l.parent.workers.Go(func() error {
			l.runSessionWorker()
			return nil
		})
	}
}

func (l *listenerLease) runRenewLoop() {
	ticker := time.NewTicker(l.renewInterval())
	defer ticker.Stop()

	for {
		select {
		case <-l.parent.ctx.Done():
			return
		case <-ticker.C:
			if l.shouldStop() {
				return
			}

			ctx, cancel := context.WithTimeout(l.parent.ctx, 10*time.Second)
			err := l.client.renewLease(ctx, l.leaseID(), l.reverseToken, l.leaseTTL)
			cancel()
			if err == nil {
				continue
			}
			if isLeaseResetError(err) || isTerminalEntryError(err) {
				l.stop(err)
				return
			}
		}
	}
}

func (l *listenerLease) runSessionWorker() {
	for {
		if l.shouldStop() {
			return
		}

		snapshot := l.sessionSnapshot()
		if !snapshot.active {
			return
		}

		conn, err := l.client.openReverseSession(l.parent.ctx, snapshot.info.LeaseID, l.reverseToken)
		if err != nil {
			if isLeaseResetError(err) || isTerminalEntryError(err) {
				l.stop(err)
				return
			}
			sleepOrDone(l.parent.ctx, defaultRetryDelay)
			continue
		}

		if err := l.parent.awaitActivation(conn, snapshot.tlsConfig, snapshot.info); err != nil {
			_ = conn.Close()
			if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
				return
			}
			if isLeaseResetError(err) || isTerminalEntryError(err) {
				l.stop(err)
				return
			}
			sleepOrDone(l.parent.ctx, defaultRetryDelay)
		}
	}
}

func (l *Listener) awaitActivation(conn net.Conn, tlsConfig *tls.Config, info ListenerEntry) error {
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
			return l.activate(conn, tlsConfig, info)
		default:
			return fmt.Errorf("unexpected reverse marker: 0x%02x", marker[0])
		}
	}
}

func (l *Listener) activate(conn net.Conn, tlsConfig *tls.Config, info ListenerEntry) error {
	if tlsConfig == nil {
		return errors.New("missing tls config")
	}

	tlsConn := tls.Server(conn, tlsConfig.Clone())
	handshakeCtx, cancel := context.WithTimeout(l.ctx, defaultHandshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		return err
	}

	select {
	case <-l.ctx.Done():
		_ = tlsConn.Close()
		return l.closeError()
	case l.accepted <- acceptedConn{conn: tlsConn, entry: info.clone()}:
		return nil
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

func (l *listenerLease) renewInterval() time.Duration {
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
	return interval
}

func (l *Listener) fail(err error) {
	l.mu.Lock()
	if err != nil && l.terminalErr == nil {
		l.terminalErr = err
	}
	l.mu.Unlock()
	l.cancel()
}

func (l *Listener) closeError() error {
	l.mu.RLock()
	terminalErr := l.terminalErr
	l.mu.RUnlock()
	if terminalErr != nil {
		return terminalErr
	}
	if err := l.ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return net.ErrClosed
		}
		return err
	}
	return net.ErrClosed
}

func (l *Listener) isClosed() bool {
	if l == nil || l.ctx == nil {
		return false
	}
	select {
	case <-l.ctx.Done():
		return true
	default:
		return false
	}
}

func (l *Listener) drainAccepted() {
	for {
		select {
		case accepted := <-l.accepted:
			if accepted.conn != nil {
				_ = accepted.conn.Close()
			}
		default:
			return
		}
	}
}

func (l *Listener) entryStopped(_ *listenerLease, err error) {
	l.mu.Lock()
	if l.activeCount > 0 {
		l.activeCount--
	}
	shouldFail := l.activeCount == 0
	l.mu.Unlock()

	if shouldFail {
		l.fail(err)
	}
}

func (l *listenerLease) snapshotInfo() (ListenerEntry, bool) {
	snapshot := l.sessionSnapshot()
	if !snapshot.active {
		return ListenerEntry{}, false
	}
	return snapshot.info, true
}

func (l *listenerLease) leaseID() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.info.LeaseID
}

func (l *listenerLease) sessionSnapshot() sessionSnapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()

	return sessionSnapshot{
		info:        l.info.clone(),
		tlsConfig:   l.tlsConfig,
		active:      l.active,
		terminalErr: l.terminalErr,
	}
}

func (l *listenerLease) shutdownState() (bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return !l.active, l.terminalErr
}

func (l *listenerLease) shouldStop() bool {
	if l == nil {
		return true
	}
	if l.parent != nil && l.parent.isClosed() {
		return true
	}
	stopped, _ := l.shutdownState()
	return stopped
}

func (l *listenerLease) stop(err error) {
	if l == nil {
		return
	}

	l.mu.Lock()
	if !l.active {
		l.mu.Unlock()
		return
	}
	l.active = false
	if err != nil && l.terminalErr == nil {
		l.terminalErr = err
	}
	l.mu.Unlock()

	if l.parent != nil {
		l.parent.entryStopped(l, err)
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

func cloneLeaseMetadata(metadata types.LeaseMetadata) types.LeaseMetadata {
	metadata.Tags = append([]string(nil), metadata.Tags...)
	return metadata
}
