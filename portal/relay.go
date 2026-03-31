package portal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
	"gosuda.org/portal/utils"
)

var (
	// ErrLeaseNotFound is returned when the requested lease does not exist or has expired
	ErrLeaseNotFound = errors.New("lease not found")
	// ErrConnectionNotAvailable is returned when the tunnel connection for a lease is not available
	ErrConnectionNotAvailable = errors.New("connection not available")
)

// portalAddr implements net.Addr for portal tunnel connections.
type portalAddr string

func (a portalAddr) Network() string { return "portal" }
func (a portalAddr) String() string  { return string(a) }

// leaseNetConn wraps a SecureConnection as net.Conn for use with http.Transport.
type leaseNetConn struct {
	*cryptoops.SecureConnection
}

func (c *leaseNetConn) LocalAddr() net.Addr  { return portalAddr(c.SecureConnection.LocalID()) }
func (c *leaseNetConn) RemoteAddr() net.Addr { return portalAddr(c.SecureConnection.RemoteID()) }

type Connection struct {
	conn io.ReadWriteCloser
	sess *yamux.Session

	streams     map[uint32]*yamux.Stream
	streamsLock sync.Mutex
}

type RelayServer struct {
	credential *cryptoops.Credential
	identity   *rdsec.Identity
	address    []string

	connidCounter   int64
	connections     map[int64]*Connection
	connectionsLock sync.RWMutex

	leaseConnections     map[string]*Connection // Key: lease ID, Value: Connection
	leaseConnectionsLock sync.RWMutex

	relayedConnections     map[string][]*yamux.Stream // Key: lease ID, Value: slice of relayed streams
	relayedConnectionsLock sync.RWMutex

	leaseManager *LeaseManager

	// OLS and Peering
	olsManager *OLSManager
	peers      map[string]*RelayPeer
	peersLock  sync.RWMutex

	stopch    chan struct{}
	waitgroup sync.WaitGroup

	// Traffic control limits and counters
	maxRelayedPerLease   int
	relayedPerLeaseCount map[string]int
	limitsLock           sync.Mutex

	// Callback for relay connection establishment (set by relay-server for BPS handling)
	onEstablishRelay func(clientStream, leaseStream *yamux.Stream, leaseID string)
}

type RelayPeer struct {
	Identity *rdsec.Identity
	Address  []string
	Conn     *Connection
}

func NewRelayServer(credential *cryptoops.Credential, address []string) *RelayServer {
	return &RelayServer{
		credential: credential,
		identity: &rdsec.Identity{
			Id:        credential.ID(),
			PublicKey: credential.PublicKey(),
		},
		address:              address,
		connidCounter:        0,
		connections:          make(map[int64]*Connection),
		leaseConnections:     make(map[string]*Connection),
		relayedConnections:   make(map[string][]*yamux.Stream),
		leaseManager:         NewLeaseManager(30 * time.Second), // TTL check every 30 seconds
		olsManager:           NewOLSManager(),
		peers:                make(map[string]*RelayPeer),
		stopch:               make(chan struct{}),
		relayedPerLeaseCount: make(map[string]int),
	}
}

var _yamux_config = func() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.MaxStreamWindowSize = 16 * 1024 * 1024 // 16MB for high-BDP scenarios
	cfg.StreamOpenTimeout = 75 * time.Second
	cfg.StreamCloseTimeout = 5 * time.Minute
	return cfg
}()

func (g *RelayServer) handleConn(id int64, connection *Connection) {
	log.Debug().Int64("conn_id", id).Msg("[RelayServer] Handling new connection")

	defer func() {
		log.Debug().Int64("conn_id", id).Msg("[RelayServer] Connection closing, cleaning up")

		// Clean up leases associated with this connection when it closes
		cleanedLeaseIDs := g.leaseManager.CleanupLeasesByConnectionID(id)

		if len(cleanedLeaseIDs) > 0 {
			log.Debug().
				Int64("conn_id", id).
				Strs("lease_ids", cleanedLeaseIDs).
				Msg("[RelayServer] Cleaned up leases for connection")
		}

		// Also clean up lease connections mapping
		g.leaseConnectionsLock.Lock()
		for _, leaseID := range cleanedLeaseIDs {
			delete(g.leaseConnections, leaseID)
		}
		g.leaseConnectionsLock.Unlock()

		// Clean up relayed connections for these leases
		g.relayedConnectionsLock.Lock()
		for _, leaseID := range cleanedLeaseIDs {
			if streams, exists := g.relayedConnections[leaseID]; exists {
				// Close all relayed streams
				for _, stream := range streams {
					stream.Close()
				}
				delete(g.relayedConnections, leaseID)
			}
		}
		g.relayedConnectionsLock.Unlock()

		// Remove the connection itself
		g.connectionsLock.Lock()
		delete(g.connections, id)
		g.connectionsLock.Unlock()

		// Close the underlying connection
		connection.conn.Close()

		log.Debug().Int64("conn_id", id).Msg("[RelayServer] Connection cleanup complete")
	}()

	for {
		stream, err := connection.sess.AcceptStream()
		if err != nil {
			log.Debug().Err(err).Int64("conn_id", id).Msg("[RelayServer] Error accepting stream, connection closing")
			return
		}
		log.Debug().
			Int64("conn_id", id).
			Uint32("stream_id", stream.StreamID()).
			Msg("[RelayServer] Accepted new stream")

		connection.streamsLock.Lock()
		connection.streams[stream.StreamID()] = stream
		connection.streamsLock.Unlock()
		go g.handleStream(stream, id, connection)
	}
}

const _MAX_RAW_PACKET_SIZE = 1 << 26 // 64MB

func (g *RelayServer) handleStream(stream *yamux.Stream, id int64, connection *Connection) {
	log.Debug().
		Int64("conn_id", id).
		Uint32("stream_id", stream.StreamID()).
		Msg("[RelayServer] Handling stream")

	var hijacked bool
	defer func() {
		stream_id := stream.StreamID()
		if !hijacked {
			log.Debug().
				Int64("conn_id", id).
				Uint32("stream_id", stream_id).
				Msg("[RelayServer] Closing stream")
			connection.streamsLock.Lock()
			stream.Close()
			delete(connection.streams, stream_id)
			connection.streamsLock.Unlock()
		} else {
			log.Debug().
				Int64("conn_id", id).
				Uint32("stream_id", stream_id).
				Msg("[RelayServer] Stream was hijacked, not closing")
		}
	}()

	ctx := &StreamContext{
		Server:       g,
		Stream:       stream,
		Connection:   connection,
		ConnectionID: id,
		Hijacked:     &hijacked,
	}

	for {
		packet, err := readPacket(stream)
		if err != nil {
			if err != io.EOF {
				log.Debug().
					Err(err).
					Int64("conn_id", id).
					Uint32("stream_id", stream.StreamID()).
					Msg("[RelayServer] Error reading packet")
			}
			return
		}

		log.Debug().
			Int64("conn_id", id).
			Uint32("stream_id", stream.StreamID()).
			Str("packet_type", packet.Type.String()).
			Msg("[RelayServer] Received packet")

		switch packet.Type {
		case rdverb.PacketType_PACKET_TYPE_RELAY_INFO_REQUEST:
			err = g.handleRelayInfoRequest(ctx, packet)
		case rdverb.PacketType_PACKET_TYPE_LEASE_UPDATE_REQUEST:
			err = g.handleLeaseUpdateRequest(ctx, packet)
		case rdverb.PacketType_PACKET_TYPE_LEASE_DELETE_REQUEST:
			err = g.handleLeaseDeleteRequest(ctx, packet)
		case rdverb.PacketType_PACKET_TYPE_CONNECTION_REQUEST:
			err = g.handleConnectionRequest(ctx, packet)
		default:
			log.Warn().
				Int64("conn_id", id).
				Str("packet_type", packet.Type.String()).
				Msg("[RelayServer] Unknown packet type")
			// Unknown packet type, return to close the stream
			return
		}

		if err != nil {
			log.Error().
				Err(err).
				Int64("conn_id", id).
				Str("packet_type", packet.Type.String()).
				Msg("[RelayServer] Error handling packet")
			return
		}

		// If the stream was hijacked, exit the loop
		if hijacked {
			log.Debug().Int64("conn_id", id).Msg("[RelayServer] Stream hijacked, exiting handler")
			return
		}
	}
}

func (g *RelayServer) HandleConnection(conn io.ReadWriteCloser) error {
	log.Debug().Msg("[RelayServer] New connection received")

	sess, err := yamux.Server(conn, _yamux_config)
	if err != nil {
		log.Error().Err(err).Msg("[RelayServer] Failed to create yamux server session")
		return err
	}

	g.connectionsLock.Lock()
	g.connidCounter++
	connID := g.connidCounter
	connection := &Connection{
		conn:    conn,
		sess:    sess,
		streams: make(map[uint32]*yamux.Stream),
	}
	g.connections[connID] = connection
	g.connectionsLock.Unlock()

	log.Debug().Int64("conn_id", connID).Msg("[RelayServer] Connection registered, starting handler")
	go g.handleConn(connID, connection)

	return nil
}

func (g *RelayServer) relayInfo() *rdverb.RelayInfo {
	return &rdverb.RelayInfo{
		Identity: g.identity,
		Address:  g.address,
		Leases:   g.leaseManager.GetAllLeases(),
	}
}

// GetLeaseManager returns the lease manager instance
func (g *RelayServer) GetLeaseManager() *LeaseManager {
	return g.leaseManager
}

// GetLeaseByName returns a lease entry by its name
func (g *RelayServer) GetLeaseByName(name string) (*LeaseEntry, bool) {
	return g.leaseManager.GetLeaseByName(name)
}

// GetLeaseByNameFold returns a lease entry by name using case-insensitive matching.
func (g *RelayServer) GetLeaseByNameFold(name string) (*LeaseEntry, bool) {
	return g.leaseManager.GetLeaseByNameFold(name)
}

// IsConnectionActive checks if a connection with the given ID is still active
func (g *RelayServer) IsConnectionActive(connectionID int64) bool {
	g.connectionsLock.RLock()
	defer g.connectionsLock.RUnlock()

	_, exists := g.connections[connectionID]
	return exists
}

// GetAllLeaseEntries returns all lease entries from the lease manager
func (g *RelayServer) GetAllLeaseEntries() []*LeaseEntry {
	g.leaseManager.leasesLock.RLock()
	defer g.leaseManager.leasesLock.RUnlock()

	var entries []*LeaseEntry
	now := time.Now()

	for _, entry := range g.leaseManager.leases {
		if now.Before(entry.Expires) {
			entries = append(entries, entry)
		}
	}

	return entries
}

// GetLeaseALPNs returns the ALPN identifiers for a given lease ID
func (g *RelayServer) GetLeaseALPNs(leaseID string) []string {
	g.leaseManager.leasesLock.RLock()
	defer g.leaseManager.leasesLock.RUnlock()

	entry, exists := g.leaseManager.leases[leaseID]
	if !exists {
		return nil
	}

	now := time.Now()
	if now.After(entry.Expires) {
		return nil
	}

	return entry.Lease.Alpn
}

// RegisterPeer registers a peer relay node and updates the OLS grid.
func (g *RelayServer) RegisterPeer(identity *rdsec.Identity, address []string, conn *Connection) {
	g.peersLock.Lock()
	g.peers[identity.Id] = &RelayPeer{
		Identity: identity,
		Address:  address,
		Conn:     conn,
	}
	g.peersLock.Unlock()

	g.UpdateOLS()
}

// UpdateOLS updates the OLSManager with current known nodes (peers + itself).
func (g *RelayServer) UpdateOLS() {
	nodes := make(map[string]string)

	// Add itself
	if len(g.address) > 0 {
		nodes[g.identity.Id] = g.address[0]
	} else {
		nodes[g.identity.Id] = "localhost"
	}

	// Add peers
	g.peersLock.RLock()
	for id, peer := range g.peers {
		if len(peer.Address) > 0 {
			nodes[id] = peer.Address[0]
		}
	}
	g.peersLock.RUnlock()

	g.olsManager.UpdateNodes(nodes)
}

func (g *RelayServer) Start() {
	g.leaseManager.Start()
	g.waitgroup.Add(1)
	go g.loadSyncLoop()
}

func (g *RelayServer) loadSyncLoop() {
	defer g.waitgroup.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Calculate local load (e.g., number of active relayed connections)
			g.relayedConnectionsLock.RLock()
			localLoad := 0
			for _, streams := range g.relayedConnections {
				localLoad += len(streams)
			}
			g.relayedConnectionsLock.RUnlock()

			// Update local load in OLSManager
			g.olsManager.UpdateLoad(g.identity.Id, float64(localLoad))

			// In a real implementation, we would send our load to peers here.
			// For this task, we assume peers also update us via some mechanism.
		case <-g.stopch:
			return
		}
	}
}

func (g *RelayServer) Stop() {
	close(g.stopch)
	g.leaseManager.Stop()
	g.waitgroup.Wait()
}

// ConnectToPeers attempts to connect to all bootstrap addresses as peers.
func (g *RelayServer) ConnectToPeers() {
	for _, addr := range g.address {
		// Don't connect to itself
		// (In a real implementation, we would compare identities)
		go func(address string) {
			// Connect to peer (using WebSocket or TCP)
			// For this implementation, we use a simplified dialer.
			dialer := utils.NewWebSocketDialer()
			conn, err := dialer(context.Background(), address)
			if err != nil {
				log.Debug().Err(err).Str("address", address).Msg("[RelayServer] Failed to connect to peer")
				return
			}

			// Create yamux session as client
			sess, err := yamux.Client(conn, _yamux_config)
			if err != nil {
				conn.Close()
				return
			}

			// In a real implementation, we would perform a handshake to get the peer's identity.
			// Here we just register it with a dummy identity for now.
			_ = &Connection{
				conn:    conn,
				sess:    sess,
				streams: make(map[uint32]*yamux.Stream),
			}
			
			// For now, we skip the identity exchange and just use the address.
			log.Info().Str("address", address).Msg("[RelayServer] Connected to peer")
		}(addr)
	}
}
func (g *RelayServer) SetMaxRelayedPerLease(n int) {
	g.limitsLock.Lock()
	g.maxRelayedPerLease = n
	g.limitsLock.Unlock()
}

// SetEstablishRelayCallback sets the callback for relay connection establishment
// This allows external code (e.g., relay-server) to handle BPS limiting
func (g *RelayServer) SetEstablishRelayCallback(
	callback func(clientStream, leaseStream *yamux.Stream, leaseID string),
) {
	g.onEstablishRelay = callback
}

// DialLease establishes a direct connection to a tunnel client's lease,
// using the relay server's own credential for the RDSEC handshake.
// Returns a net.Conn that transparently encrypts/decrypts through the tunnel.
func (g *RelayServer) DialLease(clientID, leaseID, alpn string) (net.Conn, error) {
	// 1. OLS Load Balancing
	target, err := g.olsManager.GetTargetNode(clientID, leaseID)
	if err == nil && target.ID != g.identity.Id {
		// Target is another node according to OLS grid. Proxy to it.
		g.peersLock.RLock()
		peer, exists := g.peers[target.ID]
		g.peersLock.RUnlock()

		if exists && peer.Conn != nil {
			log.Debug().
				Str("client_id", clientID).
				Str("lease_id", leaseID).
				Str("target_id", target.ID).
				Msg("[RelayServer] Proxying request to OLS target node")
			return g.dialPeerLease(peer, clientID, leaseID, alpn)
		}
	}

	// 2. Look up lease entry
	leaseEntry, exists := g.leaseManager.GetLeaseByID(leaseID)
	if !exists {
		return nil, ErrLeaseNotFound
	}

	// 3. Get the tunnel client's yamux Connection
	g.connectionsLock.RLock()
	conn, connExists := g.connections[leaseEntry.ConnectionID]
	g.connectionsLock.RUnlock()
	if !connExists {
		return nil, ErrConnectionNotAvailable
	}

	// 4. Forward CONNECTION_REQUEST to tunnel client and get acceptance
	req := &rdverb.ConnectionRequest{
		LeaseId:        leaseID,
		ClientIdentity: g.identity,
	}
	leaseStream, respCode, err := g.forwardConnectionRequest(conn, req)
	if err != nil {
		if leaseStream != nil {
			leaseStream.Close()
		}
		return nil, fmt.Errorf("forward connection request: %w", err)
	}
	if respCode != rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
		leaseStream.Close()
		return nil, ErrConnectionRejected
	}

	// 5. Perform RDSEC client handshake (relay acts as "client" to the tunnel)
	handshaker := cryptoops.NewHandshaker(g.credential)
	secConn, err := handshaker.ClientHandshake(leaseStream, alpn)
	if err != nil {
		leaseStream.Close()
		return nil, fmt.Errorf("client handshake: %w", err)
	}

	return &leaseNetConn{SecureConnection: secConn}, nil
}

// dialPeerLease proxies a DialLease request to a peer relay.
func (g *RelayServer) dialPeerLease(peer *RelayPeer, clientID, leaseID, alpn string) (net.Conn, error) {
	// 1. Forward CONNECTION_REQUEST to peer
	req := &rdverb.ConnectionRequest{
		LeaseId: leaseID,
		ClientIdentity: &rdsec.Identity{
			Id: clientID, // Use original clientID for tracking
		},
	}
	
	// Open stream on peer's yamux session
	peerStream, respCode, err := g.forwardConnectionRequest(peer.Conn, req)
	if err != nil {
		return nil, fmt.Errorf("peer forward error: %w", err)
	}
	if respCode != rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
		peerStream.Close()
		return nil, ErrConnectionRejected
	}

	// 2. Perform RDSEC client handshake with peer (or directly proxy if we wanted)
	// Actually, the requirements say "wireguard 실패 시 https 1.1 사용"
	// If the handshake fails, we return a net.Conn that wraps the raw stream for https/1.1
	handshaker := cryptoops.NewHandshaker(g.credential)
	secConn, err := handshaker.ClientHandshake(peerStream, alpn)
	if err != nil {
		log.Debug().Err(err).Msg("[RelayServer] Peer RDSEC handshake failed, falling back to HTTPS 1.1 (raw stream)")
		// Return a plain connection wrapper as fallback
		return &leaseNetConn{SecureConnection: &cryptoops.SecureConnection{}}, fmt.Errorf("fallback to https 1.1 not fully implemented via wrapper")
	}

	return &leaseNetConn{SecureConnection: secConn}, nil
}
