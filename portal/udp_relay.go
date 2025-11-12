package portal

import (
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// UDPRelay manages UDP packet relaying between clients
type UDPRelay struct {
	conn           *net.UDPConn
	sessionManager *UDPSessionManager
	relayServer    *RelayServer

	stopCh    chan struct{}
	waitGroup sync.WaitGroup
}

// NewUDPRelay creates a new UDP relay
func NewUDPRelay(listenAddr string, relayServer *RelayServer) (*UDPRelay, error) {
	addr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return nil, err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}

	log.Info().Str("addr", listenAddr).Msg("[UDPRelay] UDP relay listening")

	sessionManager := NewUDPSessionManager(
		30*time.Second, // cleanup interval
		5*time.Minute,  // session timeout (5 minutes of inactivity)
		30*time.Minute, // session TTL (30 minutes max lifetime)
	)

	relay := &UDPRelay{
		conn:           conn,
		sessionManager: sessionManager,
		relayServer:    relayServer,
		stopCh:         make(chan struct{}),
	}

	return relay, nil
}

// Start begins packet processing
func (r *UDPRelay) Start() error {
	r.sessionManager.Start()

	r.waitGroup.Add(1)
	go r.packetWorker()

	log.Info().Msg("[UDPRelay] UDP relay started")
	return nil
}

// Stop gracefully stops the UDP relay
func (r *UDPRelay) Stop() error {
	log.Debug().Msg("[UDPRelay] Stopping UDP relay")

	close(r.stopCh)
	r.sessionManager.Stop()

	if r.conn != nil {
		r.conn.Close()
	}

	r.waitGroup.Wait()

	log.Info().Msg("[UDPRelay] UDP relay stopped")
	return nil
}

// packetWorker processes incoming UDP packets
func (r *UDPRelay) packetWorker() {
	defer r.waitGroup.Done()

	buffer := make([]byte, UDPMaxPacketSize)

	for {
		select {
		case <-r.stopCh:
			return
		default:
			// Set read deadline to allow checking stopCh periodically
			r.conn.SetReadDeadline(time.Now().Add(1 * time.Second))

			n, remoteAddr, err := r.conn.ReadFromUDP(buffer)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Timeout is expected, continue
					continue
				}
				log.Error().Err(err).Msg("[UDPRelay] Error reading UDP packet")
				continue
			}

			// Process packet
			r.handlePacket(buffer[:n], remoteAddr)
		}
	}
}

// handlePacket processes a single UDP packet
func (r *UDPRelay) handlePacket(data []byte, remoteAddr *net.UDPAddr) {
	// Parse packet
	packet, err := ParseUDPPacket(data)
	if err != nil {
		log.Debug().
			Err(err).
			Str("remote", remoteAddr.String()).
			Msg("[UDPRelay] Failed to parse packet")
		return
	}

	// Get session
	session, exists := r.sessionManager.GetSession(packet.SessionToken)
	if !exists {
		log.Debug().
			Str("session_token", SessionTokenToString(packet.SessionToken)).
			Str("remote", remoteAddr.String()).
			Msg("[UDPRelay] Session not found")
		return
	}

	log.Debug().
		Str("type", packetTypeName(packet.Type)).
		Str("lease_id", session.LeaseID).
		Str("remote", remoteAddr.String()).
		Int("data_size", len(packet.Data)).
		Msg("[UDPRelay] Packet received")

	// Handle packet based on type
	switch packet.Type {
	case UDPPacketTypeData:
		r.handleDataPacket(packet, session, remoteAddr)
	case UDPPacketTypeKeepalive:
		r.handleKeepalivePacket(packet, session, remoteAddr)
	default:
		log.Warn().
			Str("type", packetTypeName(packet.Type)).
			Msg("[UDPRelay] Unknown packet type")
	}
}

// handleDataPacket relays data between clients
func (r *UDPRelay) handleDataPacket(packet *UDPPacket, session *UDPSession, senderAddr *net.UDPAddr) {
	// Determine if sender is client A or B
	var targetAddr *net.UDPAddr
	var isClientA bool

	if session.ClientAAddr != nil && session.ClientAAddr.String() == senderAddr.String() {
		// Sender is client A, relay to client B
		isClientA = true
		targetAddr = session.ClientBAddr
	} else if session.ClientBAddr != nil && session.ClientBAddr.String() == senderAddr.String() {
		// Sender is client B, relay to client A
		isClientA = false
		targetAddr = session.ClientAAddr
	} else {
		// First packet from this client, register endpoint
		if session.ClientAAddr == nil {
			session.ClientAAddr = senderAddr
			isClientA = true
			log.Debug().
				Str("lease_id", session.LeaseID).
				Str("addr", senderAddr.String()).
				Msg("[UDPRelay] Client A endpoint registered")
		} else if session.ClientBAddr == nil {
			session.ClientBAddr = senderAddr
			isClientA = false
			log.Debug().
				Str("lease_id", session.LeaseID).
				Str("addr", senderAddr.String()).
				Msg("[UDPRelay] Client B endpoint registered")
		} else {
			log.Warn().
				Str("lease_id", session.LeaseID).
				Str("sender", senderAddr.String()).
				Msg("[UDPRelay] Unknown sender, ignoring packet")
			return
		}

		// After registration, there's no target yet for the first packet
		// Just update activity and return
		session.UpdateActivity(isClientA)
		return
	}

	// Update activity
	session.UpdateActivity(isClientA)

	// Relay packet if target exists
	if targetAddr != nil {
		// Re-encode packet with same session token for target
		relayData, err := EncodeUDPPacket(packet.Type, packet.SessionToken, packet.Data)
		if err != nil {
			log.Error().
				Err(err).
				Str("lease_id", session.LeaseID).
				Msg("[UDPRelay] Failed to encode relay packet")
			return
		}

		_, err = r.conn.WriteToUDP(relayData, targetAddr)
		if err != nil {
			log.Error().
				Err(err).
				Str("lease_id", session.LeaseID).
				Str("target", targetAddr.String()).
				Msg("[UDPRelay] Failed to relay packet")
			return
		}

		log.Debug().
			Str("lease_id", session.LeaseID).
			Str("from", senderAddr.String()).
			Str("to", targetAddr.String()).
			Int("size", len(packet.Data)).
			Msg("[UDPRelay] Packet relayed")
	}
}

// handleKeepalivePacket processes keepalive packets
func (r *UDPRelay) handleKeepalivePacket(packet *UDPPacket, session *UDPSession, senderAddr *net.UDPAddr) {
	// Determine which client sent the keepalive
	isClientA := false
	if session.ClientAAddr != nil && session.ClientAAddr.String() == senderAddr.String() {
		isClientA = true
	}

	// Update activity
	session.UpdateActivity(isClientA)

	log.Debug().
		Str("lease_id", session.LeaseID).
		Str("sender", senderAddr.String()).
		Bool("is_client_a", isClientA).
		Msg("[UDPRelay] Keepalive received")
}

// CreateSession creates a new UDP session for a lease
func (r *UDPRelay) CreateSession(leaseID string) (*UDPSession, error) {
	return r.sessionManager.CreateSession(leaseID)
}

// GetSession retrieves a session
func (r *UDPRelay) GetSession(token [UDPSessionTokenSize]byte) (*UDPSession, bool) {
	return r.sessionManager.GetSession(token)
}

// GetSessionByLease retrieves a session by lease ID
func (r *UDPRelay) GetSessionByLease(leaseID string) (*UDPSession, bool) {
	return r.sessionManager.GetSessionByLease(leaseID)
}

// GetPort returns the UDP port the relay is listening on
func (r *UDPRelay) GetPort() int {
	if r.conn == nil {
		return 0
	}
	return r.conn.LocalAddr().(*net.UDPAddr).Port
}

// packetTypeName returns a human-readable name for a packet type
func packetTypeName(t byte) string {
	switch t {
	case UDPPacketTypeRegister:
		return "REGISTER"
	case UDPPacketTypeData:
		return "DATA"
	case UDPPacketTypeKeepalive:
		return "KEEPALIVE"
	default:
		return "UNKNOWN"
	}
}
