package portal

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// udpSession tracks one client endpoint sending to a per-lease UDP listener.
type udpSession struct {
	FlowID   uint32
	Addr     *net.UDPAddr
	LastSeen time.Time
}

// udpRelay binds a UDP port for a lease and relays datagrams bidirectionally
// between raw UDP clients and the tunnel's QUIC connection via the quicBroker.
type udpRelay struct {
	leaseID string
	port    int
	broker  *quicBroker
	conn    *net.UDPConn

	sessions map[string]*udpSession // "ip:port" → session
	mu       sync.Mutex

	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

func newUDPRelay(leaseID string, port int, broker *quicBroker) *udpRelay {
	return &udpRelay{
		leaseID:  leaseID,
		port:     port,
		broker:   broker,
		sessions: make(map[string]*udpSession),
		done:     make(chan struct{}),
	}
}

// Start binds the UDP port and launches read/write relay goroutines.
func (r *udpRelay) Start(ctx context.Context) error {
	addr := &net.UDPAddr{Port: r.port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("listen udp :%d: %w", r.port, err)
	}
	r.conn = conn

	relayCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	go r.readLoop(relayCtx)
	go r.writeLoop(relayCtx)
	go r.sessionCleanup(relayCtx)

	log.Info().
		Str("component", "udp-relay").
		Str("lease_id", r.leaseID).
		Int("port", r.port).
		Msg("udp relay started")

	return nil
}

// Stop closes the UDP socket and cancels the relay context.
func (r *udpRelay) Stop() {
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.conn != nil {
			_ = r.conn.Close()
		}
		close(r.done)
		log.Info().
			Str("component", "udp-relay").
			Str("lease_id", r.leaseID).
			Int("port", r.port).
			Msg("udp relay stopped")
	})
}

// readLoop reads raw UDP from public clients and forwards via QUIC DATAGRAM.
func (r *udpRelay) readLoop(ctx context.Context) {
	buf := make([]byte, defaultMaxDatagramSize)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_ = r.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, clientAddr, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return
		}

		flowID := r.getOrCreateFlow(clientAddr)
		payload := make([]byte, n)
		copy(payload, buf[:n])

		if err := r.broker.SendDatagram(flowID, payload); err != nil {
			// Tunnel not connected yet — drop silently.
			continue
		}
	}
}

// writeLoop receives QUIC DATAGRAM frames from the tunnel and sends raw UDP back.
func (r *udpRelay) writeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-r.broker.Incoming():
			addr, ok := r.broker.LookupFlowAddr(frame.FlowID)
			if !ok {
				continue
			}
			_, _ = r.conn.WriteToUDP(frame.Payload, addr)
		}
	}
}

func (r *udpRelay) getOrCreateFlow(addr *net.UDPAddr) uint32 {
	key := addr.String()

	r.mu.Lock()
	defer r.mu.Unlock()

	if s, ok := r.sessions[key]; ok {
		s.LastSeen = time.Now()
		return s.FlowID
	}

	flowID := r.broker.AllocateFlow(addr)
	r.sessions[key] = &udpSession{
		FlowID:   flowID,
		Addr:     addr,
		LastSeen: time.Now(),
	}
	return flowID
}

// sessionCleanup periodically removes idle UDP sessions.
func (r *udpRelay) sessionCleanup(ctx context.Context) {
	ticker := time.NewTicker(defaultUDPSessionTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			r.mu.Lock()
			for key, s := range r.sessions {
				if now.Sub(s.LastSeen) > defaultUDPSessionTimeout {
					delete(r.sessions, key)
				}
			}
			r.mu.Unlock()
		}
	}
}
