package relaydns

import (
	"io"
	"sync"
	"time"

	"github.com/gosuda/relaydns/relaydns/core/cryptoops"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdsec"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdverb"
	"github.com/hashicorp/yamux"
)

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

	stopch    chan struct{}
	waitgroup sync.WaitGroup
}

func NewRelayServer(credential *cryptoops.Credential, address []string) *RelayServer {
	return &RelayServer{
		credential: credential,
		identity: &rdsec.Identity{
			Id:        credential.ID(),
			PublicKey: credential.PublicKey(),
		},
		address:            address,
		connidCounter:      0,
		connections:        make(map[int64]*Connection),
		leaseConnections:   make(map[string]*Connection),
		relayedConnections: make(map[string][]*yamux.Stream),
		leaseManager:       NewLeaseManager(30 * time.Second), // TTL check every 30 seconds
		stopch:             make(chan struct{}),
	}
}

var _yamux_config = yamux.DefaultConfig()

func (g *RelayServer) handleConn(id int64, connection *Connection) {
	defer func() {
		// Clean up leases associated with this connection when it closes
		cleanedLeaseIDs := g.leaseManager.CleanupLeasesByConnectionID(id)

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
	}()

	for {
		stream, err := connection.sess.AcceptStream()
		if err != nil {
			return
		}
		connection.streamsLock.Lock()
		connection.streams[stream.StreamID()] = stream
		connection.streamsLock.Unlock()
		go g.handleStream(stream, id, connection)
	}
}

const _MAX_RAW_PACKET_SIZE = 1 << 26 // 64MB

func (g *RelayServer) handleStream(stream *yamux.Stream, id int64, connection *Connection) {
	var hijacked bool = false
	defer func() {
		stream_id := stream.StreamID()
		if !hijacked {
			connection.streamsLock.Lock()
			stream.Close()
			delete(connection.streams, stream_id)
			connection.streamsLock.Unlock()
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
			return
		}

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
			// Unknown packet type, return to close the stream
			return
		}

		if err != nil {
			return
		}

		// If the stream was hijacked, exit the loop
		if hijacked {
			return
		}
	}
}

func (g *RelayServer) HandleConnection(conn io.ReadWriteCloser) error {
	sess, err := yamux.Server(conn, _yamux_config)
	if err != nil {
		return err
	}

	g.connectionsLock.Lock()
	g.connidCounter++
	connection := &Connection{
		conn:    conn,
		sess:    sess,
		streams: make(map[uint32]*yamux.Stream),
	}
	g.connections[g.connidCounter] = connection
	g.connectionsLock.Unlock()

	go g.handleConn(g.connidCounter, connection)

	return nil
}

func (g *RelayServer) relayInfo() *rdverb.RelayInfo {
	leases := g.leaseManager.GetAllLeases()
	var leaseIds []string
	for _, lease := range leases {
		leaseIds = append(leaseIds, string(lease.Identity.Id))
	}

	return &rdverb.RelayInfo{
		Identity: g.identity,
		Address:  g.address,
		Leases:   leaseIds,
	}
}

// GetLeaseManager returns the lease manager instance
func (g *RelayServer) GetLeaseManager() *LeaseManager {
	return g.leaseManager
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

func (g *RelayServer) Start() {
	g.leaseManager.Start()
}

func (g *RelayServer) Stop() {
	close(g.stopch)
	g.leaseManager.Stop()
	g.waitgroup.Wait()
}
