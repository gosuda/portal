package portal

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
)

type Connection struct {
	sess Session

	streams     map[int64]Stream
	streamsLock sync.Mutex
}

type RelayServer struct {
	credential *cryptoops.Credential
	identity   *rdsec.Identity
	address    []string

	connidCounter   int64
	connections     map[int64]*Connection
	connectionsLock sync.RWMutex

	relayedConnections     map[string][]Stream // Key: lease ID, Value: slice of relayed streams
	relayedConnectionsLock sync.RWMutex

	leaseManager *LeaseManager

	stopch         chan struct{}
	waitgroup      sync.WaitGroup
	registrationMu sync.Mutex
	stopping       atomic.Bool

	// Traffic control limits and counters
	maxRelayedPerLease   int
	relayedPerLeaseCount map[string]int
	limitsLock           sync.Mutex

	// Callback for relay connection establishment (set by relay-server for BPS handling)
	onEstablishRelay func(clientStream, leaseStream Stream, leaseID string)
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
		relayedConnections:   make(map[string][]Stream),
		leaseManager:         NewLeaseManager(30 * time.Second), // TTL check every 30 seconds
		stopch:               make(chan struct{}),
		relayedPerLeaseCount: make(map[string]int),
	}
}

func (g *RelayServer) registerWorker() bool {
	g.registrationMu.Lock()
	defer g.registrationMu.Unlock()

	if g.stopping.Load() {
		return false
	}

	g.waitgroup.Add(1)
	return true
}

func (g *RelayServer) waitForRegistrationBarrier() {
	g.registrationMu.Lock()
	// Observe state while holding the registration mutex; this acts as a synchronization barrier
	// for any in-flight registerWorker critical sections.
	_ = g.stopping.Load()
	g.registrationMu.Unlock()
}

func (g *RelayServer) handleConn(id int64, connection *Connection) {
	defer g.waitgroup.Done()
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

		// Clean up relayed connections for these leases
		g.relayedConnectionsLock.Lock()
		for _, leaseID := range cleanedLeaseIDs {
			if streams, exists := g.relayedConnections[leaseID]; exists {
				// Close all relayed streams
				for _, stream := range streams {
					closeWithLog(stream, "[RelayServer] Failed to close relayed stream during cleanup")
				}
				delete(g.relayedConnections, leaseID)
			}
		}
		g.relayedConnectionsLock.Unlock()

		// Remove the connection itself
		g.connectionsLock.Lock()
		delete(g.connections, id)
		g.connectionsLock.Unlock()

		// Close the session (and underlying transport)
		closeWithLog(connection.sess, "[RelayServer] Failed to close session during cleanup")

		log.Debug().Int64("conn_id", id).Msg("[RelayServer] Connection cleanup complete")
	}()

	var streamSeq int64
	for {
		if g.stopping.Load() {
			log.Debug().Int64("conn_id", id).Msg("[RelayServer] Server stopping, no longer accepting streams")
			return
		}

		stream, err := connection.sess.AcceptStream(context.Background())
		if err != nil {
			log.Debug().Err(err).Int64("conn_id", id).Msg("[RelayServer] Error accepting stream, connection closing")
			return
		}

		if g.stopping.Load() {
			closeWithLog(stream, "[RelayServer] Failed to close stream while stopping")
			log.Debug().
				Int64("conn_id", id).
				Msg("[RelayServer] Server stopping after stream accept, closing stream")
			return
		}

		streamSeq++
		streamID := streamSeq

		log.Debug().
			Int64("conn_id", id).
			Int64("stream_id", streamID).
			Msg("[RelayServer] Accepted new stream")

		connection.streamsLock.Lock()
		connection.streams[streamID] = stream
		connection.streamsLock.Unlock()

		if !g.registerWorker() {
			connection.streamsLock.Lock()
			delete(connection.streams, streamID)
			connection.streamsLock.Unlock()
			closeWithLog(stream, "[RelayServer] Failed to close stream while stopping")
			log.Debug().
				Int64("conn_id", id).
				Int64("stream_id", streamID).
				Msg("[RelayServer] Server stopping before stream handler launch, closing stream")
			return
		}

		go func(stream Stream, streamID int64) {
			defer g.waitgroup.Done()
			g.handleStream(stream, streamID, id, connection)
		}(stream, streamID)
	}
}

const maxRawPacketSize = 1 << 26 // 64MB

func (g *RelayServer) handleStream(stream Stream, streamID, connID int64, connection *Connection) {
	log.Debug().
		Int64("conn_id", connID).
		Int64("stream_id", streamID).
		Msg("[RelayServer] Handling stream")

	var hijacked bool
	defer func() {
		if !hijacked {
			log.Debug().
				Int64("conn_id", connID).
				Int64("stream_id", streamID).
				Msg("[RelayServer] Closing stream")
			connection.streamsLock.Lock()
			delete(connection.streams, streamID)
			connection.streamsLock.Unlock()
			closeWithLog(stream, "[RelayServer] Failed to close stream")
		} else {
			log.Debug().
				Int64("conn_id", connID).
				Int64("stream_id", streamID).
				Msg("[RelayServer] Stream was hijacked, not closing")
		}
	}()

	ctx := &StreamContext{
		Server:       g,
		Stream:       stream,
		Connection:   connection,
		ConnectionID: connID,
		Hijacked:     &hijacked,
	}

	for {
		packet, err := readPacket(stream)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Debug().
					Err(err).
					Int64("conn_id", connID).
					Int64("stream_id", streamID).
					Msg("[RelayServer] Error reading packet")
			}
			return
		}

		log.Debug().
			Int64("conn_id", connID).
			Int64("stream_id", streamID).
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
				Int64("conn_id", connID).
				Str("packet_type", packet.Type.String()).
				Msg("[RelayServer] Unknown packet type")
			// Unknown packet type, return to close the stream
			return
		}

		if err != nil {
			log.Error().
				Err(err).
				Int64("conn_id", connID).
				Str("packet_type", packet.Type.String()).
				Msg("[RelayServer] Error handling packet")
			return
		}

		// If the stream was hijacked, exit the loop
		if hijacked {
			log.Debug().Int64("conn_id", connID).Msg("[RelayServer] Stream hijacked, exiting handler")
			return
		}
	}
}

// HandleSession registers a multiplexed session and starts handling its streams.
func (g *RelayServer) HandleSession(sess Session) {
	log.Debug().Msg("[RelayServer] New session received")

	g.connectionsLock.Lock()
	if g.stopping.Load() {
		g.connectionsLock.Unlock()
		closeWithLog(sess, "[RelayServer] Failed to close incoming session while stopping")
		log.Debug().Msg("[RelayServer] Rejected incoming session while stopping")
		return
	}

	g.connidCounter++
	connID := g.connidCounter
	connection := &Connection{
		sess:    sess,
		streams: make(map[int64]Stream),
	}
	g.connections[connID] = connection

	if !g.registerWorker() {
		delete(g.connections, connID)
		g.connectionsLock.Unlock()
		closeWithLog(sess, "[RelayServer] Failed to close incoming session while stopping")
		log.Debug().Msg("[RelayServer] Rejected incoming session while stopping")
		return
	}

	g.connectionsLock.Unlock()

	log.Debug().Int64("conn_id", connID).Msg("[RelayServer] Connection registered, starting handler")
	go g.handleConn(connID, connection)
}

func (g *RelayServer) relayInfo() *rdverb.RelayInfo {
	return &rdverb.RelayInfo{
		Identity: g.identity,
		Address:  g.address,
		Leases:   g.leaseManager.GetAllLeases(),
	}
}

// GetLeaseManager returns the lease manager instance.
func (g *RelayServer) GetLeaseManager() *LeaseManager {
	return g.leaseManager
}

// GetLeaseByName returns a lease entry by its name.
func (g *RelayServer) GetLeaseByName(name string) (*LeaseEntry, bool) {
	return g.leaseManager.GetLeaseByName(name)
}

// IsConnectionActive checks if a connection with the given ID is still active.
func (g *RelayServer) IsConnectionActive(connectionID int64) bool {
	g.connectionsLock.RLock()
	defer g.connectionsLock.RUnlock()

	_, exists := g.connections[connectionID]
	return exists
}

// GetAllLeaseEntries returns all lease entries from the lease manager.
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

// GetLeaseALPNs returns the ALPN identifiers for a given lease ID.
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

func (g *RelayServer) Start() {
	g.leaseManager.Start()
}

func (g *RelayServer) Stop() {
	if !g.stopping.CompareAndSwap(false, true) {
		return
	}

	close(g.stopch)
	g.leaseManager.Stop()

	// Close all active sessions to unblock handleConn goroutines
	// waiting in AcceptStream. This must happen before Wait() to
	// avoid deadlock.
	g.connectionsLock.RLock()
	for _, conn := range g.connections {
		closeWithLog(conn.sess, "[RelayServer] Failed to close active session during server stop")
	}
	g.connectionsLock.RUnlock()

	// Ensure in-flight waitgroup registrations complete before waiting.
	g.waitForRegistrationBarrier()

	g.waitgroup.Wait()
}

// Traffic control setters.
func (g *RelayServer) SetMaxRelayedPerLease(n int) {
	g.limitsLock.Lock()
	g.maxRelayedPerLease = n
	g.limitsLock.Unlock()
}

// SetEstablishRelayCallback sets the callback for relay connection establishment
// This allows external code (e.g., relay-server) to handle BPS limiting.
func (g *RelayServer) SetEstablishRelayCallback(
	callback func(clientStream, leaseStream Stream, leaseID string),
) {
	g.onEstablishRelay = callback
}
