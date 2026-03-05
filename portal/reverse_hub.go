package portal

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/types"
)

const (
	// QueueSize is the maximum number of pending reverse connections per lease.
	QueueSize = 64

	// DefaultAcquireTimeout is the default timeout for acquiring a reverse connection.
	DefaultAcquireTimeout = 2 * time.Second

	// TLSAcquireWait is the timeout for TLS passthrough connections.
	TLSAcquireWait = 2 * time.Second

	// AuthFailureDelay is the delay before closing unauthorized connections (rate limiting).
	AuthFailureDelay = 2 * time.Second

	// controlWriteTimeout bounds control-marker writes on reverse connections.
	controlWriteTimeout = 2 * time.Second

	// ReverseIdleKeepaliveInterval sends an idle keepalive byte to reduce
	// reverse connection disconnections from intermediate idle timeouts.
	ReverseIdleKeepaliveInterval = 25 * time.Second
)

// ReverseConn wraps a net.Conn with lifecycle management for the connection pool.
type ReverseConn struct {
	Conn   net.Conn
	done   chan struct{}
	active chan struct{}
	once   sync.Once
	// activateOnce ensures active channel is closed exactly once.
	activateOnce sync.Once
	// writeMu serializes writes while the connection is idle.
	writeMu sync.Mutex
	// closed tracks local close to help queue consumers skip stale entries.
	closed atomic.Bool
}

// NewReverseConn creates a new pooled connection.
func NewReverseConn(conn net.Conn) *ReverseConn {
	return &ReverseConn{
		Conn:   conn,
		done:   make(chan struct{}),
		active: make(chan struct{}),
	}
}

// Close closes the connection and signals completion.
func (c *ReverseConn) Close() {
	c.closed.Store(true)
	_ = c.Conn.Close()
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

func (c *ReverseConn) Activate() {
	if c == nil {
		return
	}
	c.activateOnce.Do(func() {
		close(c.active)
	})
}

func (c *ReverseConn) WriteControlByte(marker byte, timeout time.Duration) error {
	if c == nil || c.Conn == nil {
		return net.ErrClosed
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if timeout > 0 {
		_ = c.Conn.SetWriteDeadline(time.Now().Add(timeout))
		defer c.Conn.SetWriteDeadline(time.Time{})
	}
	_, err := c.Conn.Write([]byte{marker})
	return err
}

type ReverseHub struct {
	pools        map[string]chan *ReverseConn
	dropped      map[string]struct{}
	authorizer   func(leaseID, token string) bool
	ipBanChecker func(ip string) bool
	onAccepted   func(leaseID, ip string)
	mu           sync.RWMutex
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

// SetIPBanChecker sets optional IP ban check for reverse connections.
func (h *ReverseHub) SetIPBanChecker(checker func(ip string) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ipBanChecker = checker
}

// SetOnAccepted sets optional callback for authorized reverse connections.
func (h *ReverseHub) SetOnAccepted(onAccepted func(leaseID, ip string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onAccepted = onAccepted
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

func (h *ReverseHub) isIPBanned(ip string) bool {
	h.mu.RLock()
	checker := h.ipBanChecker
	h.mu.RUnlock()
	ip = strings.TrimSpace(ip)
	if checker == nil || ip == "" {
		return false
	}
	return checker(ip)
}

func (h *ReverseHub) notifyAccepted(leaseID, ip string) {
	h.mu.RLock()
	onAccepted := h.onAccepted
	h.mu.RUnlock()
	if onAccepted == nil {
		return
	}
	onAccepted(leaseID, ip)
}

func (h *ReverseHub) Offer(leaseID string, conn *ReverseConn) bool {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" || conn == nil || conn.Conn == nil {
		return false
	}

	pool := h.getOrCreatePool(leaseID)
	if pool == nil {
		return false
	}

	for range QueueSize + 1 {
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
	leaseID = strings.TrimSpace(leaseID)

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
			// Stop idle keepalive and signal tunnel worker to release this connection.
			conn.Activate()
			err := conn.WriteControlByte(types.TLSStartMarker, controlWriteTimeout)
			if err == nil {
				return conn, nil
			}

			log.Warn().
				Err(err).
				Str("lease_id", leaseID).
				Msg("[ReverseHub] Failed to send TLS start marker; retrying with new connection")
			conn.Close()
			continue
		case <-timer.C:
			return nil, fmt.Errorf("tunnel acquisition timeout for lease %s", leaseID)
		}
	}
}

func (h *ReverseHub) DropLease(leaseID string) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return
	}

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
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return
	}

	h.mu.Lock()
	delete(h.dropped, leaseID)
	h.mu.Unlock()
}

func (h *ReverseHub) HandleConnect(conn net.Conn, leaseID, token, remoteIP string) {
	if conn == nil {
		return
	}

	leaseID = strings.TrimSpace(leaseID)
	token = strings.TrimSpace(token)
	remoteIP = strings.TrimSpace(remoteIP)

	if leaseID == "" {
		log.Warn().Msg("[ReverseHub] Missing lease_id on reverse connect")
		h.rejectConn(conn, "[ReverseHub] failed to close unauthorized reverse connection")
		return
	}

	if h.isIPBanned(remoteIP) {
		log.Warn().
			Str("lease_id", leaseID).
			Str("ip", remoteIP).
			Msg("[ReverseHub] IP banned for reverse connect")
		h.rejectConn(conn, "[ReverseHub] failed to close banned reverse connection")
		return
	}

	if !h.isAuthorized(leaseID, token) {
		log.Warn().Str("lease_id", leaseID).Msg("[ReverseHub] Unauthorized reverse connect")
		h.rejectConn(conn, "[ReverseHub] failed to close unauthorized reverse connection")
		return
	}

	h.notifyAccepted(leaseID, remoteIP)
	reverseConn := NewReverseConn(conn)
	if !h.Offer(leaseID, reverseConn) {
		log.Warn().Str("lease_id", leaseID).Msg("[ReverseHub] Connection pool full for lease")
		reverseConn.Close()
		return
	}

	h.keepAliveWhileIdle(reverseConn, leaseID)

	// Wait until the connection is used and closed
	reverseConn.Wait()
}

func (h *ReverseHub) rejectConn(conn net.Conn, debugCloseMessage string) {
	time.Sleep(AuthFailureDelay)
	h.closeConn(conn, debugCloseMessage)
}

func (h *ReverseHub) closeConn(conn net.Conn, debugCloseMessage string) {
	if conn == nil {
		return
	}
	if err := conn.Close(); err != nil {
		log.Debug().Err(err).Msg(debugCloseMessage)
	}
}

func (h *ReverseHub) keepAliveWhileIdle(conn *ReverseConn, leaseID string) {
	ticker := time.NewTicker(ReverseIdleKeepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-conn.done:
			return
		case <-conn.active:
			return
		case <-ticker.C:
			if err := conn.WriteControlByte(types.ReverseKeepaliveMarker, controlWriteTimeout); err != nil {
				log.Debug().
					Err(err).
					Str("lease_id", leaseID).
					Msg("[ReverseHub] Idle keepalive write failed")
				conn.Close()
				return
			}
		}
	}
}
