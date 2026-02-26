package portal

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/websocket"
)

const (
	// HTTPStartMarker is sent by the relay to activate a reverse connection
	// for HTTP proxy mode.
	HTTPStartMarker = byte(0x01)

	// TLSStartMarker is sent by the relay to activate a reverse connection
	// for TLS passthrough mode.
	TLSStartMarker = byte(0x02)

	// QueueSize is the maximum number of pending reverse connections per lease.
	QueueSize = 64

	// DefaultAcquireTimeout is the default timeout for acquiring a reverse connection.
	DefaultAcquireTimeout = 2 * time.Second

	// HTTPProxyWait is the timeout for HTTP proxy connections (shorter for better UX).
	HTTPProxyWait = 1500 * time.Millisecond

	// TLSAcquireWait is the timeout for TLS passthrough connections.
	TLSAcquireWait = 2 * time.Second

	// AuthFailureDelay is the delay before closing unauthorized connections (rate limiting).
	AuthFailureDelay = 2 * time.Second
)

// ReverseConn wraps a net.Conn with lifecycle management for the connection pool.
type ReverseConn struct {
	Conn net.Conn
	done chan struct{}
	once sync.Once
	// closed tracks local close to help queue consumers skip stale entries.
	closed atomic.Bool
}

// NewReverseConn creates a new pooled connection.
func NewReverseConn(conn net.Conn) *ReverseConn {
	return &ReverseConn{
		Conn: conn,
		done: make(chan struct{}),
	}
}

// Close closes the connection and signals completion.
func (c *ReverseConn) Close() {
	c.closed.Store(true)
	c.Conn.Close()
	c.once.Do(func() {
		close(c.done)
	})
}

// Wait blocks until the connection is closed.
func (c *ReverseConn) Wait() {
	<-c.done
}

func (c *ReverseConn) IsClosed() bool {
	return c == nil || c.closed.Load()
}

type ReverseHub struct {
	mu         sync.RWMutex
	pools      map[string]chan *ReverseConn
	dropped    map[string]struct{}
	authorizer func(leaseID, token string) bool
}

// NewReverseHub creates a new reverse connection hub.
func NewReverseHub() *ReverseHub {
	return &ReverseHub{
		pools:   make(map[string]chan *ReverseConn),
		dropped: make(map[string]struct{}),
	}
}

func (h *ReverseHub) getOrCreatePool(leaseID string) chan *ReverseConn {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, dropped := h.dropped[leaseID]; dropped {
		return nil
	}

	if pool, ok := h.pools[leaseID]; ok {
		return pool
	}
	pool := make(chan *ReverseConn, QueueSize)
	h.pools[leaseID] = pool
	return pool
}

func (h *ReverseHub) getPool(leaseID string) (chan *ReverseConn, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	pool, ok := h.pools[leaseID]
	return pool, ok
}

// SetAuthorizer sets the authentication function for new connections.
func (h *ReverseHub) SetAuthorizer(authorizer func(leaseID, token string) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.authorizer = authorizer
}

func (h *ReverseHub) isAuthorized(leaseID, token string) bool {
	h.mu.RLock()
	authorizer := h.authorizer
	h.mu.RUnlock()
	if authorizer == nil {
		return false
	}
	return authorizer(leaseID, token)
}

func (h *ReverseHub) Offer(leaseID string, conn *ReverseConn) bool {
	pool := h.getOrCreatePool(leaseID)
	if pool == nil {
		return false
	}

	for i := 0; i < QueueSize+1; i++ {
		select {
		case pool <- conn:
			return true
		default:
		}

		// Pool full: evict one oldest entry and retry.
		select {
		case old := <-pool:
			if old != nil {
				old.Close()
			}
		default:
		}
	}

	return false
}

func (h *ReverseHub) AcquireForTLS(leaseID string, timeout time.Duration) (*ReverseConn, error) {
	return h.acquireWithStartMarker(leaseID, timeout, TLSStartMarker, "TLS")
}

// AcquireForHTTP retrieves a connection for HTTP proxy mode.
// A mode-specific start marker is sent before returning the connection.
func (h *ReverseHub) AcquireForHTTP(leaseID string, timeout time.Duration) (*ReverseConn, error) {
	return h.acquireWithStartMarker(leaseID, timeout, HTTPStartMarker, "HTTP")
}

func (h *ReverseHub) acquireWithStartMarker(leaseID string, timeout time.Duration, marker byte, mode string) (*ReverseConn, error) {
	pool, ok := h.getPool(leaseID)
	if !ok {
		return nil, fmt.Errorf("no tunnel available for lease %s", leaseID)
	}

	if timeout <= 0 {
		timeout = DefaultAcquireTimeout
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("tunnel acquisition timeout for lease %s", leaseID)
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(remaining)

		select {
		case conn := <-pool:
			if conn == nil || conn.IsClosed() {
				continue
			}
			// Signal tunnel worker to release this connection to application Accept().
			_ = conn.Conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			_, err := conn.Conn.Write([]byte{marker})
			_ = conn.Conn.SetWriteDeadline(time.Time{})
			if err == nil {
				return conn, nil
			}

			log.Warn().
				Err(err).
				Str("lease_id", leaseID).
				Str("mode", mode).
				Msg("[ReverseHub] Failed to send start marker; retrying with new connection")
			conn.Close()
			continue
		case <-timer.C:
			return nil, fmt.Errorf("tunnel acquisition timeout for lease %s", leaseID)
		}
	}
}

func (h *ReverseHub) DropLease(leaseID string) {
	h.mu.Lock()
	pool, ok := h.pools[leaseID]
	if ok {
		delete(h.pools, leaseID)
	}
	h.dropped[leaseID] = struct{}{}
	h.mu.Unlock()

	if !ok {
		return
	}

	// Drain and close pending connections
	for {
		select {
		case conn := <-pool:
			if conn != nil {
				conn.Close()
			}
		default:
			return
		}
	}
}

// ClearDropped removes a lease from the dropped set, allowing it to be re-registered.
// This should be called when a lease is re-registered after being dropped.
func (h *ReverseHub) ClearDropped(leaseID string) {
	h.mu.Lock()
	delete(h.dropped, leaseID)
	h.mu.Unlock()
}

func (h *ReverseHub) HandleConnect(ws *websocket.Conn) {
	ws.PayloadType = websocket.BinaryFrame

	req := ws.Request()
	leaseID := ""
	token := ""
	if req != nil {
		leaseID = strings.TrimSpace(req.URL.Query().Get("lease_id"))
		token = strings.TrimSpace(req.URL.Query().Get("token"))
	}

	if leaseID == "" {
		log.Warn().Msg("[ReverseHub] Missing lease_id on reverse connect")
		time.Sleep(AuthFailureDelay)
		ws.Close()
		return
	}

	if !h.isAuthorized(leaseID, token) {
		log.Warn().Str("lease_id", leaseID).Msg("[ReverseHub] Unauthorized reverse connect")
		time.Sleep(AuthFailureDelay)
		ws.Close()
		return
	}

	conn := NewReverseConn(ws)
	if !h.Offer(leaseID, conn) {
		log.Warn().Str("lease_id", leaseID).Msg("[ReverseHub] Connection pool full for lease")
		conn.Close()
		return
	}

	// Wait until the connection is used and closed
	conn.Wait()
}
