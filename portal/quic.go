package portal

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/types"
)

var (
	errQUICNoConnection  = errors.New("no quic connection registered")
	errQUICAlreadyClosed = errors.New("quic broker closed")
)

// quicBroker manages a single QUIC connection from a tunnel for one lease.
// All UDP traffic for the lease is multiplexed over DATAGRAM frames on this
// connection, identified by flow IDs.
type quicBroker struct {
	leaseID string

	conn      *quic.Conn
	flowTable map[uint32]*net.UDPAddr // flowID → client addr
	addrIndex map[string]uint32       // "ip:port" → flowID
	nextFlow  uint32

	incoming chan types.DatagramFrame // frames received from tunnel
	done     chan struct{}

	mu        sync.Mutex
	closeOnce sync.Once
	closed    bool
}

func newQUICBroker(leaseID string) *quicBroker {
	return &quicBroker{
		leaseID:   leaseID,
		flowTable: make(map[uint32]*net.UDPAddr),
		addrIndex: make(map[string]uint32),
		nextFlow:  1,
		incoming:  make(chan types.DatagramFrame, 256),
		done:      make(chan struct{}),
	}
}

// Register stores the QUIC connection from the tunnel for this lease.
// Replaces any existing connection.
func (b *quicBroker) Register(conn *quic.Conn) {
	b.mu.Lock()
	old := b.conn
	b.conn = conn
	b.mu.Unlock()

	if old != nil {
		_ = old.CloseWithError(0, "replaced")
	}

	go b.receiveLoop(conn)

	log.Info().
		Str("component", "quic-broker").
		Str("lease_id", b.leaseID).
		Str("remote_addr", conn.RemoteAddr().String()).
		Msg("quic tunnel connection registered")
}

// HasConnection reports whether a tunnel QUIC connection is active.
func (b *quicBroker) HasConnection() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conn != nil && !b.closed
}

// SendDatagram encodes a flow-framed datagram and sends it to the tunnel.
func (b *quicBroker) SendDatagram(flowID uint32, payload []byte) error {
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()

	if conn == nil {
		return errQUICNoConnection
	}
	return conn.SendDatagram(types.EncodeDatagram(flowID, payload))
}

// Incoming returns the channel that delivers datagrams received from the tunnel.
func (b *quicBroker) Incoming() <-chan types.DatagramFrame {
	return b.incoming
}

// Done returns a channel closed when the broker shuts down.
func (b *quicBroker) Done() <-chan struct{} {
	return b.done
}

// AllocateFlow assigns a flow ID for a client address. If the address already
// has a flow, the existing ID is returned.
func (b *quicBroker) AllocateFlow(addr *net.UDPAddr) uint32 {
	key := addr.String()

	b.mu.Lock()
	defer b.mu.Unlock()

	if id, ok := b.addrIndex[key]; ok {
		return id
	}
	id := b.nextFlow
	b.nextFlow++
	b.flowTable[id] = addr
	b.addrIndex[key] = id
	return id
}

// LookupFlowAddr returns the client address for a flow ID.
func (b *quicBroker) LookupFlowAddr(flowID uint32) (*net.UDPAddr, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	addr, ok := b.flowTable[flowID]
	return addr, ok
}

// Stop tears down the QUIC connection and signals done.
func (b *quicBroker) Stop() {
	b.closeOnce.Do(func() {
		b.mu.Lock()
		b.closed = true
		conn := b.conn
		b.conn = nil
		b.mu.Unlock()

		if conn != nil {
			_ = conn.CloseWithError(0, "lease stopped")
		}
		close(b.done)
	})
}

func (b *quicBroker) receiveLoop(conn *quic.Conn) {
	for {
		data, err := conn.ReceiveDatagram(context.Background())
		if err != nil {
			b.mu.Lock()
			// Only clear if this is still the active connection.
			if b.conn == conn {
				b.conn = nil
			}
			b.mu.Unlock()

			if !b.isClosed() {
				log.Warn().
					Err(err).
					Str("component", "quic-broker").
					Str("lease_id", b.leaseID).
					Msg("quic receive loop ended")
			}
			return
		}

		frame, err := types.DecodeDatagram(data)
		if err != nil {
			continue
		}

		select {
		case b.incoming <- frame:
		default:
			// Drop if channel full — back-pressure on tunnel.
		}
	}
}

func (b *quicBroker) isClosed() bool {
	select {
	case <-b.done:
		return true
	default:
		return false
	}
}

// quicControlMessage is sent by the tunnel on the first QUIC stream after
// connecting. The relay reads it to associate the connection with a lease.
type quicControlMessage struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
}

// quicTunnelListener manages the QUIC listener that accepts tunnel connections
// on the relay API UDP port.
type quicTunnelListener struct {
	listener *quic.Listener
	server   *Server

	done      chan struct{}
	closeOnce sync.Once
}

func newQUICTunnelListener(listener *quic.Listener, server *Server) *quicTunnelListener {
	return &quicTunnelListener{
		listener: listener,
		server:   server,
		done:     make(chan struct{}),
	}
}

func (l *quicTunnelListener) run() error {
	for {
		conn, err := l.listener.Accept(context.Background())
		if err != nil {
			select {
			case <-l.done:
				return nil
			default:
			}
			if errors.Is(err, quic.ErrServerClosed) {
				return nil
			}
			return err
		}
		go l.handleConnection(conn)
	}
}

func (l *quicTunnelListener) handleConnection(conn *quic.Conn) {
	stream, err := conn.AcceptStream(context.Background())
	if err != nil {
		_ = conn.CloseWithError(1, "stream accept failed")
		return
	}

	// Read control message with timeout.
	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	var msg quicControlMessage
	buf := make([]byte, 4096)
	n, err := stream.Read(buf)
	if err != nil {
		_ = conn.CloseWithError(1, "control read failed")
		return
	}

	// Simple JSON decode.
	if decErr := json.Unmarshal(buf[:n], &msg); decErr != nil {
		_ = conn.CloseWithError(1, "invalid control message")
		return
	}
	_ = stream.SetReadDeadline(time.Time{})

	lease, err := l.server.findLeaseByID(msg.LeaseID)
	if err != nil {
		_, _ = stream.Write([]byte(`{"ok":false,"error":"lease_not_found"}`))
		_ = conn.CloseWithError(1, "lease not found")
		return
	}

	if authErr := l.server.authorizeLeaseToken(lease, msg.ReverseToken); authErr != nil {
		_, _ = stream.Write([]byte(`{"ok":false,"error":"unauthorized"}`))
		_ = conn.CloseWithError(1, "unauthorized")
		return
	}

	if lease.QUICBroker == nil {
		_, _ = stream.Write([]byte(`{"ok":false,"error":"transport_mismatch"}`))
		_ = conn.CloseWithError(1, "lease does not support QUIC transport")
		return
	}

	// Success — register the QUIC connection with the broker.
	lease.QUICBroker.Register(conn)

	// Confirm registration to the tunnel.
	_, _ = stream.Write([]byte(`{"ok":true}`))

	l.server.registry.Touch(lease.ID, conn.RemoteAddr().String(), time.Now())

	log.Info().
		Str("component", "quic-tunnel-listener").
		Str("lease_id", lease.ID).
		Str("lease_name", lease.Name).
		Str("remote_addr", conn.RemoteAddr().String()).
		Msg("quic tunnel connected")
}

func (l *quicTunnelListener) close() error {
	var closeErr error
	l.closeOnce.Do(func() {
		close(l.done)
		closeErr = l.listener.Close()
	})
	return closeErr
}
