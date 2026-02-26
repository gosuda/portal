package portal

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ReverseQueueSize is the maximum number of pre-established reverse connections
// queued per lease.
const ReverseQueueSize = 64

// reverseStartMarker is the single byte sent to signal the tunnel that
// the reverse connection has been acquired and data forwarding will begin.
const reverseStartMarker = 0x01

// Sentinel errors for ReverseHub operations.
var (
	ErrReverseHubStopped = errors.New("reverse hub stopped")
	ErrNoReverseConn     = errors.New("no reverse connection available")
	ErrUnauthorized      = errors.New("unauthorized reverse connection")
)

// ReverseConn represents a single pre-established reverse connection
// from a tunnel to the relay. The relay acquires it when a browser
// connects and signals the tunnel with a start marker.
type ReverseConn struct {
	Conn net.Conn
	done chan struct{}
	once sync.Once
}

// Close closes the underlying connection and signals the done channel.
// Safe to call multiple times.
func (rc *ReverseConn) Close() error {
	var err error
	rc.once.Do(func() {
		close(rc.done)
		err = rc.Conn.Close()
	})
	return err
}

// Wait blocks until the ReverseConn is closed.
func (rc *ReverseConn) Wait() {
	<-rc.done
}

// NewReverseConn wraps a net.Conn as a ReverseConn.
func NewReverseConn(conn net.Conn) *ReverseConn {
	return &ReverseConn{
		Conn: conn,
		done: make(chan struct{}),
	}
}

// ReverseHub manages queues of pre-established reverse connections
// from tunnels. The SNI router acquires connections from these queues when
// a browser connects to a tunnel subdomain.
type ReverseHub struct {
	mu      sync.Mutex
	pending map[string]chan *ReverseConn // lease_id -> buffered channel

	authorizer func(leaseID, token string) bool

	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewReverseHub creates a new ReverseHub.
func NewReverseHub() *ReverseHub {
	return &ReverseHub{
		pending: make(map[string]chan *ReverseConn),
		stopCh:  make(chan struct{}),
	}
}

// SetAuthorizer sets the authorization function for validating reverse
// connection requests. The function receives lease_id and token, and
// must use subtle.ConstantTimeCompare for token comparison.
func (h *ReverseHub) SetAuthorizer(fn func(leaseID, token string) bool) {
	h.mu.Lock()
	h.authorizer = fn
	h.mu.Unlock()
}

// Offer enqueues a reverse connection for the given lease. Non-blocking:
// returns false if the queue is full. The caller MUST close conn if Offer
// returns false.
func (h *ReverseHub) Offer(leaseID string, conn *ReverseConn) bool {
	ch := h.getOrCreate(leaseID)
	select {
	case ch <- conn:
		return true
	default:
		return false
	}
}

// AcquireStarted dequeues a reverse connection for the given lease,
// writes the start marker (0x01), and returns the connection.
// Retries on stale connections (write fails). Blocks until a connection
// is available or the timeout expires.
func (h *ReverseHub) AcquireStarted(leaseID string, timeout time.Duration) (*ReverseConn, error) {
	ch := h.get(leaseID)
	if ch == nil {
		return nil, ErrNoReverseConn
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-h.stopCh:
			return nil, ErrReverseHubStopped
		case <-timer.C:
			return nil, ErrNoReverseConn
		case conn, ok := <-ch:
			if !ok {
				return nil, ErrNoReverseConn
			}
			// Write start marker.
			_, err := conn.Conn.Write([]byte{reverseStartMarker})
			if err != nil {
				// Stale connection, close and retry.
				log.Debug().Str("lease_id", leaseID).Err(err).Msg("[ReverseHub] stale connection, retrying")
				_ = conn.Close()
				continue
			}
			return conn, nil
		}
	}
}

// DropLease removes the queue for a lease and drains+closes all pending connections.
func (h *ReverseHub) DropLease(leaseID string) {
	h.mu.Lock()
	ch, exists := h.pending[leaseID]
	if exists {
		delete(h.pending, leaseID)
	}
	h.mu.Unlock()

	if !exists {
		return
	}

	// Drain and close all pending connections.
	for {
		select {
		case conn := <-ch:
			_ = conn.Close()
		default:
			return
		}
	}
}

// HandleConnect handles the HTTP upgrade for a reverse connection from a tunnel.
// Query params: lease_id, token.
// After authorization, the HTTP connection is hijacked to obtain a raw net.Conn.
// The connection is offered to the queue and the handler blocks until completion.
func (h *ReverseHub) HandleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	leaseID := r.URL.Query().Get("lease_id")
	token := r.URL.Query().Get("token")

	if leaseID == "" || token == "" {
		http.Error(w, `{"success":false,"message":"missing lease_id or token"}`, http.StatusBadRequest)
		return
	}

	// Authorize.
	h.mu.Lock()
	authorizer := h.authorizer
	h.mu.Unlock()

	if authorizer != nil && !authorizer(leaseID, token) {
		log.Warn().Str("lease_id", leaseID).Msg("[ReverseHub] unauthorized connect attempt")
		http.Error(w, `{"success":false,"message":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	// Hijack the HTTP connection to get the raw net.Conn.
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}

	netConn, bufrw, err := hj.Hijack()
	if err != nil {
		log.Error().Err(err).Str("lease_id", leaseID).Msg("[ReverseHub] hijack failed")
		return
	}

	// Send 101 Switching Protocols to signal successful upgrade.
	if _, err := bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: portal-reverse/1\r\nConnection: Upgrade\r\n\r\n"); err != nil {
		_ = netConn.Close()
		return
	}
	if err := bufrw.Flush(); err != nil {
		_ = netConn.Close()
		return
	}

	// Wrap with buffered reader to handle any data consumed during HTTP parsing.
	var rawConn net.Conn = netConn
	if bufrw.Reader.Buffered() > 0 {
		rawConn = &bufferedConn{Conn: netConn, r: bufrw.Reader}
	}

	conn := NewReverseConn(rawConn)

	if !h.Offer(leaseID, conn) {
		log.Warn().Str("lease_id", leaseID).Msg("[ReverseHub] reverse queue full")
		_ = conn.Close()
		return
	}

	// Block until the connection is consumed and completed.
	conn.Wait()
}

// Stop shuts down the ReverseHub and drains all queues.
func (h *ReverseHub) Stop() {
	h.stopOnce.Do(func() {
		close(h.stopCh)
	})

	h.mu.Lock()
	leaseIDs := make([]string, 0, len(h.pending))
	for id := range h.pending {
		leaseIDs = append(leaseIDs, id)
	}
	h.mu.Unlock()

	for _, id := range leaseIDs {
		h.DropLease(id)
	}
}

// getOrCreate returns the buffered channel for a lease, creating it if needed.
func (h *ReverseHub) getOrCreate(leaseID string) chan *ReverseConn {
	h.mu.Lock()
	defer h.mu.Unlock()

	ch, exists := h.pending[leaseID]
	if !exists {
		ch = make(chan *ReverseConn, ReverseQueueSize)
		h.pending[leaseID] = ch
	}
	return ch
}

// get returns the buffered channel for a lease, or nil if not found.
func (h *ReverseHub) get(leaseID string) chan *ReverseConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pending[leaseID]
}

// bufferedConn wraps a net.Conn with a bufio.Reader so that any data
// consumed into the buffer during HTTP parsing is not lost.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}
