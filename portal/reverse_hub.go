package portal

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/websocket"
)

const (
	ReverseStartMarker    = byte(0x01)
	ReverseQueueSize      = 64
	ReverseAcquireWait    = 2 * time.Second
	ReverseHTTPWait       = 1500 * time.Millisecond
	ReverseSNIAcquireWait = 2 * time.Second
)

type ReverseConn struct {
	Conn net.Conn
	done chan struct{}
	once sync.Once
}

func NewReverseConn(conn net.Conn) *ReverseConn {
	return &ReverseConn{
		Conn: conn,
		done: make(chan struct{}),
	}
}

func (c *ReverseConn) Close() {
	c.Conn.Close()
	c.once.Do(func() {
		close(c.done)
	})
}

func (c *ReverseConn) Wait() {
	<-c.done
}

type ReverseHub struct {
	mu         sync.RWMutex
	pending    map[string]chan *ReverseConn
	authorizer func(string, string) bool
}

func NewReverseHub() *ReverseHub {
	return &ReverseHub{
		pending: make(map[string]chan *ReverseConn),
	}
}

func (h *ReverseHub) getOrCreate(leaseID string) chan *ReverseConn {
	h.mu.Lock()
	defer h.mu.Unlock()

	ch, ok := h.pending[leaseID]
	if ok {
		return ch
	}
	ch = make(chan *ReverseConn, ReverseQueueSize)
	h.pending[leaseID] = ch
	return ch
}

func (h *ReverseHub) get(leaseID string) (chan *ReverseConn, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ch, ok := h.pending[leaseID]
	return ch, ok
}

func (h *ReverseHub) SetAuthorizer(authorizer func(string, string) bool) {
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
	ch := h.getOrCreate(leaseID)
	select {
	case ch <- conn:
		return true
	default:
		return false
	}
}

func (h *ReverseHub) Acquire(leaseID string, timeout time.Duration) (*ReverseConn, error) {
	ch, ok := h.get(leaseID)
	if !ok {
		return nil, fmt.Errorf("no reverse tunnel for lease %s", leaseID)
	}

	if timeout <= 0 {
		timeout = ReverseAcquireWait
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case conn := <-ch:
		if conn == nil {
			return nil, fmt.Errorf("reverse tunnel unavailable for lease %s", leaseID)
		}
		return conn, nil
	case <-timer.C:
		return nil, fmt.Errorf("reverse tunnel timeout for lease %s", leaseID)
	}
}

func (h *ReverseHub) AcquireStarted(leaseID string, timeout time.Duration) (*ReverseConn, error) {
	if timeout <= 0 {
		timeout = ReverseAcquireWait
	}

	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("reverse tunnel timeout for lease %s", leaseID)
		}

		conn, err := h.Acquire(leaseID, remaining)
		if err != nil {
			return nil, err
		}

		_ = conn.Conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, err = conn.Conn.Write([]byte{ReverseStartMarker})
		_ = conn.Conn.SetWriteDeadline(time.Time{})
		if err == nil {
			return conn, nil
		}

		log.Warn().
			Err(err).
			Str("lease_id", leaseID).
			Msg("[ReverseHub] Failed to start reverse stream; retrying")
		conn.Close()
	}
}

func (h *ReverseHub) DropLease(leaseID string) {
	h.mu.Lock()
	ch, ok := h.pending[leaseID]
	if ok {
		delete(h.pending, leaseID)
	}
	h.mu.Unlock()

	if !ok {
		return
	}

	for {
		select {
		case conn := <-ch:
			if conn != nil {
				conn.Close()
			}
		default:
			return
		}
	}
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
		ws.Close()
		return
	}
	if !h.isAuthorized(leaseID, token) {
		log.Warn().Str("lease_id", leaseID).Msg("[ReverseHub] Unauthorized reverse connect")
		ws.Close()
		return
	}

	conn := NewReverseConn(ws)
	if !h.Offer(leaseID, conn) {
		log.Warn().Str("lease_id", leaseID).Msg("[ReverseHub] Reverse queue full")
		conn.Close()
		return
	}

	conn.Wait()
}
