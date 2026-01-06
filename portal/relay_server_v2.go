package portal

import (
	"context"
	"encoding/hex"
	"io"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/xtaci/kcp-go/v5"

	"gosuda.org/portal/portal/corev2/common"
	"gosuda.org/portal/portal/corev2/identity"
	"gosuda.org/portal/portal/corev2/serdes"
)

// RelayServerV2 handles V2 relay protocol with KCP multipath support
type RelayServerV2 struct {
	credential     *identity.Credential
	cert           *identity.CertificateV2
	relayID        common.RelayID
	bootstrapAddrs []string

	// Lease management
	leaseManager *LeaseManagerV2

	// Connection management (V2 uses KCP sessions)
	kcpSessions     map[common.SessionID]*RelaySessionV2
	kcpSessionsLock sync.RWMutex

	// Control
	stopch    chan struct{}
	waitgroup sync.WaitGroup

	// Callbacks
	onNewLease     func(*LeaseV2)
	onLeaseExpired func(common.SessionID)
	onConnection   func(*RelaySessionV2)
}

// RelaySessionV2 represents a V2 relay session (KCP-based)
type RelaySessionV2 struct {
	SessionID  common.SessionID
	KCPConn    *kcp.UDPSession
	RemoteAddr net.Addr
	LastSeen   time.Time
	Expires    time.Time

	// Metrics
	BytesSent     uint64
	BytesReceived uint64
	PacketsSent   uint64
	PacketsRecv   uint64

	closed bool
	mu     sync.RWMutex
}

// NewRelayServerV2 creates a new V2 relay server
func NewRelayServerV2(cred *identity.Credential, bootstrapAddrs []string) (*RelayServerV2, error) {
	// Create certificate
	cert, err := identity.NewCertificateV2(cred, uint64(time.Now().Add(24*time.Hour).Unix()), nil)
	if err != nil {
		return nil, err
	}

	// Derive relay ID
	pubKey := cred.PublicKeyArray()
	relayID := common.RelayID{}
	copy(relayID[:], pubKey[:common.RelayIDSize])

	return &RelayServerV2{
		credential:     cred,
		cert:           cert,
		relayID:        relayID,
		bootstrapAddrs: bootstrapAddrs,
		leaseManager:   NewLeaseManagerV2(30 * time.Second),
		kcpSessions:    make(map[common.SessionID]*RelaySessionV2),
		stopch:         make(chan struct{}),
	}, nil
}

// Start starts the relay server
func (r *RelayServerV2) Start() {
	r.leaseManager.Start()
	log.Info().Msg("[RelayServerV2] Server started")
}

// Stop stops the relay server
func (r *RelayServerV2) Stop() {
	close(r.stopch)
	r.leaseManager.Stop()

	// Close all KCP sessions
	r.kcpSessionsLock.Lock()
	for _, sess := range r.kcpSessions {
		sess.Close()
	}
	r.kcpSessions = make(map[common.SessionID]*RelaySessionV2)
	r.kcpSessionsLock.Unlock()

	r.waitgroup.Wait()
	log.Info().Msg("[RelayServerV2] Server stopped")
}

// HandleV2Packet handles an incoming V2 packet over KCP
func (r *RelayServerV2) HandleV2Packet(ctx context.Context, kcpConn *kcp.UDPSession, remoteAddr net.Addr) error {
	// Read V2 packet from KCP connection
	buf := make([]byte, 1500)
	n, err := kcpConn.Read(buf)
	if err != nil {
		if err != io.EOF {
			log.Debug().Err(err).Str("remote", remoteAddr.String()).Msg("[RelayServerV2] Read error")
		}
		return err
	}

	// Deserialize V2 packet
	pkt, err := serdes.DeserializePacket(buf[:n])
	if err != nil {
		log.Debug().Err(err).Str("remote", remoteAddr.String()).Msg("[RelayServerV2] Packet deserialization error")
		return err
	}

	log.Debug().
		Uint8("type", pkt.Header.Type).
		Str("session", sessionIDToString(pkt.Header.SessionID())).
		Msg("[RelayServerV2] Received packet")

	// Route based on packet type
	switch pkt.Header.Type {
	case common.MsgLeaseRegisterReq:
		return r.handleLeaseRegisterReq(ctx, kcpConn, remoteAddr, pkt)
	case common.MsgLeaseRefreshReq:
		return r.handleLeaseRefreshReq(ctx, kcpConn, remoteAddr, pkt)
	case common.MsgLeaseDeleteReq:
		return r.handleLeaseDeleteReq(ctx, kcpConn, remoteAddr, pkt)
	case common.TypeDataKCP:
		return r.handleDataKCP(ctx, kcpConn, pkt)
	case common.TypeSAInit:
		return r.handleSAInit(ctx, kcpConn, remoteAddr, pkt)
	default:
		log.Warn().Uint8("type", pkt.Header.Type).Msg("[RelayServerV2] Unknown packet type")
		return common.ErrInvalidType
	}
}

// handleLeaseRegisterReq handles lease registration request
func (r *RelayServerV2) handleLeaseRegisterReq(ctx context.Context, kcpConn *kcp.UDPSession, remoteAddr net.Addr, pkt *serdes.Packet) error {
	// Deserialize request
	req, err := DeserializeLeaseRegisterRequest(pkt.Payload)
	if err != nil {
		return r.sendErrorResponse(kcpConn, pkt.Header, common.StatusInvalidArgument)
	}

	log.Info().
		Str("session", sessionIDToString(req.SessionID)).
		Str("name", req.Name).
		Msg("[RelayServerV2] Lease registration request")

	// Register lease
	resp, err := r.leaseManager.RegisterLease(req, 0)
	if err != nil {
		log.Error().Err(err).Msg("[RelayServerV2] Lease registration failed")
		return r.sendErrorResponse(kcpConn, pkt.Header, common.StatusInvalidArgument)
	}

	// Send response
	respData, err := SerializeLeaseRegisterResponse(resp)
	if err != nil {
		return err
	}

	responsePkt := serdes.NewPacket(
		serdes.NewHeader(common.MsgLeaseRegisterResp, pkt.Header.SessionID(), pkt.Header.PathID, pkt.Header.PktSeq),
		respData,
	)

	return r.writePacket(kcpConn, responsePkt)
}

// handleLeaseRefreshReq handles lease refresh request
func (r *RelayServerV2) handleLeaseRefreshReq(ctx context.Context, kcpConn *kcp.UDPSession, remoteAddr net.Addr, pkt *serdes.Packet) error {
	// Deserialize request
	req, err := DeserializeLeaseRefreshRequest(pkt.Payload)
	if err != nil {
		return r.sendErrorResponse(kcpConn, pkt.Header, common.StatusInvalidArgument)
	}

	log.Debug().
		Str("session", sessionIDToString(req.SessionID)).
		Msg("[RelayServerV2] Lease refresh request")

	// Refresh lease
	resp, err := r.leaseManager.RefreshLease(req, 0)
	if err != nil {
		return r.sendErrorResponse(kcpConn, pkt.Header, common.StatusInvalidArgument)
	}

	// Send response
	respData, err := SerializeLeaseRefreshResponse(resp)
	if err != nil {
		return err
	}

	responsePkt := serdes.NewPacket(
		serdes.NewHeader(common.MsgLeaseRefreshResp, pkt.Header.SessionID(), pkt.Header.PathID, pkt.Header.PktSeq),
		respData,
	)

	return r.writePacket(kcpConn, responsePkt)
}

// handleLeaseDeleteReq handles lease delete request
func (r *RelayServerV2) handleLeaseDeleteReq(ctx context.Context, kcpConn *kcp.UDPSession, remoteAddr net.Addr, pkt *serdes.Packet) error {
	// Deserialize request
	req, err := DeserializeLeaseDeleteRequest(pkt.Payload)
	if err != nil {
		return r.sendErrorResponse(kcpConn, pkt.Header, common.StatusInvalidArgument)
	}

	log.Info().
		Str("session", sessionIDToString(req.SessionID)).
		Msg("[RelayServerV2] Lease delete request")

	// Delete lease
	resp, err := r.leaseManager.DeleteLease(req)
	if err != nil {
		return r.sendErrorResponse(kcpConn, pkt.Header, common.StatusInvalidArgument)
	}

	// Remove session
	r.removeSession(req.SessionID)

	// Send response
	respData, err := SerializeLeaseDeleteResponse(resp)
	if err != nil {
		return err
	}

	responsePkt := serdes.NewPacket(
		serdes.NewHeader(common.MsgLeaseDeleteResp, pkt.Header.SessionID(), pkt.Header.PathID, pkt.Header.PktSeq),
		respData,
	)

	return r.writePacket(kcpConn, responsePkt)
}

// handleDataKCP handles data KCP packet
func (r *RelayServerV2) handleDataKCP(ctx context.Context, kcpConn *kcp.UDPSession, pkt *serdes.Packet) error {
	sessionID := pkt.Header.SessionID()

	// Find session
	session := r.getSession(sessionID)
	if session == nil {
		log.Debug().Str("session", sessionIDToString(sessionID)).Msg("[RelayServerV2] Session not found")
		return nil
	}

	// Update session stats
	session.mu.Lock()
	session.BytesReceived += uint64(len(pkt.Payload))
	session.PacketsRecv++
	session.LastSeen = time.Now()
	session.mu.Unlock()

	// TODO: Route data to target application/lease
	log.Debug().
		Str("session", sessionIDToString(sessionID)).
		Int("payload_size", len(pkt.Payload)).
		Msg("[RelayServerV2] Data packet received")

	return nil
}

// handleSAInit handles session initialization
func (r *RelayServerV2) handleSAInit(ctx context.Context, kcpConn *kcp.UDPSession, remoteAddr net.Addr, pkt *serdes.Packet) error {
	sessionID := pkt.Header.SessionID()

	log.Info().
		Str("session", sessionIDToString(sessionID)).
		Msg("[RelayServerV2] Session initialization request")

	// Create new session
	session := &RelaySessionV2{
		SessionID:  sessionID,
		KCPConn:    kcpConn,
		RemoteAddr: remoteAddr,
		LastSeen:   time.Now(),
		Expires:    time.Now().Add(common.LeaseTTL),
	}

	// Add to sessions map
	r.addSession(session)

	// Call callback
	if r.onConnection != nil {
		r.onConnection(session)
	}

	// Send acknowledgment
	responsePkt := serdes.NewPacket(
		serdes.NewHeader(common.TypeSAResp, sessionID, 0, 0),
		nil,
	)

	return r.writePacket(kcpConn, responsePkt)
}

// sendErrorResponse sends an error response
func (r *RelayServerV2) sendErrorResponse(kcpConn *kcp.UDPSession, header *serdes.Header, status uint8) error {
	responsePkt := serdes.NewPacket(
		serdes.NewHeader(header.Type, header.SessionID(), header.PathID, header.PktSeq),
		[]byte{status},
	)

	return r.writePacket(kcpConn, responsePkt)
}

// writePacket writes a V2 packet over KCP
func (r *RelayServerV2) writePacket(kcpConn *kcp.UDPSession, pkt *serdes.Packet) error {
	buf := make([]byte, pkt.SerializeSize())
	if err := pkt.Serialize(buf); err != nil {
		return err
	}

	_, err := kcpConn.Write(buf)
	return err
}

// addSession adds a session
func (r *RelayServerV2) addSession(session *RelaySessionV2) {
	r.kcpSessionsLock.Lock()
	defer r.kcpSessionsLock.Unlock()
	r.kcpSessions[session.SessionID] = session
	log.Debug().Str("session", sessionIDToString(session.SessionID)).Msg("[RelayServerV2] Session added")
}

// removeSession removes a session
func (r *RelayServerV2) removeSession(sessionID common.SessionID) {
	r.kcpSessionsLock.Lock()
	defer r.kcpSessionsLock.Unlock()

	if sess, exists := r.kcpSessions[sessionID]; exists {
		sess.Close()
		delete(r.kcpSessions, sessionID)
		log.Debug().Str("session", sessionIDToString(sessionID)).Msg("[RelayServerV2] Session removed")
	}
}

// getSession retrieves a session
func (r *RelayServerV2) getSession(sessionID common.SessionID) *RelaySessionV2 {
	r.kcpSessionsLock.RLock()
	defer r.kcpSessionsLock.RUnlock()
	return r.kcpSessions[sessionID]
}

// Close closes a session
func (s *RelaySessionV2) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	if s.KCPConn != nil {
		s.KCPConn.Close()
	}

	return nil
}

// IsClosed checks if session is closed
func (s *RelaySessionV2) IsClosed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

// GetLeaseManager returns lease manager
func (r *RelayServerV2) GetLeaseManager() *LeaseManagerV2 {
	return r.leaseManager
}

// GetLeaseByName retrieves a lease by name
func (r *RelayServerV2) GetLeaseByName(name string) (*LeaseV2Entry, bool) {
	return r.leaseManager.GetLeaseByName(name)
}

// GetAllLeaseEntries returns all lease entries
func (r *RelayServerV2) GetAllLeaseEntries() []*LeaseV2Entry {
	r.leaseManager.leasesLock.RLock()
	defer r.leaseManager.leasesLock.RUnlock()

	var entries []*LeaseV2Entry
	now := time.Now()

	for _, entry := range r.leaseManager.leases {
		if now.Before(entry.Lease.Expires) {
			entries = append(entries, entry)
		}
	}

	return entries
}

// SetOnNewLease sets callback for new lease
func (r *RelayServerV2) SetOnNewLease(cb func(*LeaseV2)) {
	r.onNewLease = cb
}

// SetOnLeaseExpired sets callback for expired lease
func (r *RelayServerV2) SetOnLeaseExpired(cb func(common.SessionID)) {
	r.onLeaseExpired = cb
}

// SetOnConnection sets callback for new connection
func (r *RelayServerV2) SetOnConnection(cb func(*RelaySessionV2)) {
	r.onConnection = cb
}

// GetRelayID returns relay ID
func (r *RelayServerV2) GetRelayID() common.RelayID {
	return r.relayID
}

// GetCertificate returns relay certificate
func (r *RelayServerV2) GetCertificate() *identity.CertificateV2 {
	return r.cert
}

// GetBootstrapAddrs returns bootstrap addresses
func (r *RelayServerV2) GetBootstrapAddrs() []string {
	return r.bootstrapAddrs
}

// GetSessionCount returns number of active sessions
func (r *RelayServerV2) GetSessionCount() int {
	r.kcpSessionsLock.RLock()
	defer r.kcpSessionsLock.RUnlock()
	return len(r.kcpSessions)
}

// GetStats returns server statistics
func (r *RelayServerV2) GetStats() map[string]interface{} {
	r.kcpSessionsLock.RLock()
	defer r.kcpSessionsLock.RUnlock()

	totalBytesSent := uint64(0)
	totalBytesRecv := uint64(0)
	totalPacketsSent := uint64(0)
	totalPacketsRecv := uint64(0)

	for _, sess := range r.kcpSessions {
		totalBytesSent += sess.BytesSent
		totalBytesRecv += sess.BytesReceived
		totalPacketsSent += sess.PacketsSent
		totalPacketsRecv += sess.PacketsRecv
	}

	return map[string]interface{}{
		"active_sessions":    len(r.kcpSessions),
		"total_bytes_sent":   totalBytesSent,
		"total_bytes_recv":   totalBytesRecv,
		"total_packets_sent": totalPacketsSent,
		"total_packets_recv": totalPacketsRecv,
	}
}

// sessionIDToString converts SessionID to hex string for logging
func sessionIDToString(sid common.SessionID) string {
	return hex.EncodeToString(sid[:])
}

// relayIDToString converts RelayID to hex string for logging
func relayIDToString(rid common.RelayID) string {
	return hex.EncodeToString(rid[:])
}
