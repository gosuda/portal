package portal

import (
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
)

type Connection struct {
	conn io.ReadWriteCloser
	sess *yamux.Session

	streams     map[uint32]*yamux.Stream
	streamsLock sync.Mutex
}

// relayCmd is the command interface for RelayServer event loop.
type relayCmd interface{ relayCmd() }

// Connection management commands
type cmdRegisterConn struct {
	id   int64
	conn *Connection
	done chan<- struct{}
}

type cmdRemoveConn struct {
	id   int64
	done chan<- []string // returns cleaned lease IDs
}

type cmdIsConnActive struct {
	id    int64
	reply chan<- bool
}

type cmdGetConnByLeaseEntry struct {
	connID int64
	reply  chan<- *Connection
}

// Lease connection management commands
type cmdRegisterLeaseConn struct {
	leaseID string
	conn    *Connection
	done    chan<- struct{}
}

type cmdUnregisterLeaseConn struct {
	leaseID string
	done    chan<- struct{}
}

// Relayed connection tracking commands
type cmdAddRelayed struct {
	leaseID string
	stream  *yamux.Stream
	done    chan<- struct{}
}

type cmdRemoveRelayed struct {
	leaseID string
	stream  *yamux.Stream
	done    chan<- struct{}
}

// Limit management commands
type cmdCheckAndIncLimit struct {
	leaseID string
	reply   chan<- bool // true if under limit and incremented
}

type cmdDecLimit struct {
	leaseID string
	done    chan<- struct{}
}

type cmdSetMaxRelayed struct {
	max  int
	done chan<- struct{}
}

// Command interface markers
func (cmdRegisterConn) relayCmd()        {}
func (cmdRemoveConn) relayCmd()          {}
func (cmdIsConnActive) relayCmd()        {}
func (cmdGetConnByLeaseEntry) relayCmd() {}
func (cmdRegisterLeaseConn) relayCmd()   {}
func (cmdUnregisterLeaseConn) relayCmd() {}
func (cmdAddRelayed) relayCmd()          {}
func (cmdRemoveRelayed) relayCmd()       {}
func (cmdCheckAndIncLimit) relayCmd()    {}
func (cmdDecLimit) relayCmd()            {}
func (cmdSetMaxRelayed) relayCmd()       {}

// RelayServer handles relay connections using a single-threaded event loop.
// All state mutations are processed sequentially via the command channel.
type RelayServer struct {
	credential *cryptoops.Credential
	identity   *rdsec.Identity
	address    []string

	connidCounter      int64
	connections        map[int64]*Connection
	leaseConnections   map[string]*Connection     // Key: lease ID, Value: Connection
	relayedConnections map[string][]*yamux.Stream // Key: lease ID, Value: slice of relayed streams

	leaseManager *LeaseManager

	// Event loop
	cmdCh chan relayCmd
	runWg sync.WaitGroup

	stopch    chan struct{}
	waitgroup sync.WaitGroup

	// Traffic control limits and counters
	maxRelayedPerLease   int
	relayedPerLeaseCount map[string]int

	// Callback for relay connection establishment (set by relay-server for BPS handling)
	onEstablishRelay func(clientStream, leaseStream *yamux.Stream, leaseID string)
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
		cmdCh:                make(chan relayCmd, 256),
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

		// Remove connection and clean up all associated state via event loop
		done := make(chan []string, 1)
		g.cmdCh <- &cmdRemoveConn{id: id, done: done}
		cleanedLeaseIDs := <-done

		if len(cleanedLeaseIDs) > 0 {
			log.Debug().
				Int64("conn_id", id).
				Strs("lease_ids", cleanedLeaseIDs).
				Msg("[RelayServer] Cleaned up leases for connection")
		}

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

	connID := atomic.AddInt64(&g.connidCounter, 1)
	connection := &Connection{
		conn:    conn,
		sess:    sess,
		streams: make(map[uint32]*yamux.Stream),
	}

	// Register connection via event loop
	done := make(chan struct{}, 1)
	g.cmdCh <- &cmdRegisterConn{id: connID, conn: connection, done: done}
	<-done

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

// IsConnectionActive checks if a connection with the given ID is still active
func (g *RelayServer) IsConnectionActive(connectionID int64) bool {
	reply := make(chan bool, 1)
	g.cmdCh <- &cmdIsConnActive{id: connectionID, reply: reply}
	return <-reply
}

// GetAllLeaseEntries returns all lease entries from the lease manager
func (g *RelayServer) GetAllLeaseEntries() []*LeaseEntry {
	return g.leaseManager.GetAllEntries()
}

// GetLeaseALPNs returns the ALPN identifiers for a given lease ID
func (g *RelayServer) GetLeaseALPNs(leaseID string) []string {
	return g.leaseManager.GetLeaseALPNs(leaseID)
}

func (g *RelayServer) Start() {
	g.runWg.Add(1)
	go g.run()
	g.leaseManager.Start()
}

func (g *RelayServer) Stop() {
	close(g.stopch)
	g.runWg.Wait()
	g.leaseManager.Stop()
	g.waitgroup.Wait()
}

// run is the main event loop that processes all commands sequentially.
func (g *RelayServer) run() {
	defer g.runWg.Done()
	for {
		select {
		case cmd := <-g.cmdCh:
			g.handleCmd(cmd)
		case <-g.stopch:
			return
		}
	}
}

// handleCmd dispatches commands to their handlers.
func (g *RelayServer) handleCmd(cmd relayCmd) {
	switch c := cmd.(type) {
	case *cmdRegisterConn:
		g.connections[c.id] = c.conn
		c.done <- struct{}{}

	case *cmdRemoveConn:
		delete(g.connections, c.id)
		// Clean up leases associated with this connection
		cleanedLeaseIDs := g.leaseManager.CleanupLeasesByConnectionID(c.id)
		// Clean up lease connections and relayed connections
		for _, leaseID := range cleanedLeaseIDs {
			delete(g.leaseConnections, leaseID)
			if streams, exists := g.relayedConnections[leaseID]; exists {
				for _, stream := range streams {
					stream.Close()
				}
				delete(g.relayedConnections, leaseID)
			}
			delete(g.relayedPerLeaseCount, leaseID)
		}
		c.done <- cleanedLeaseIDs

	case *cmdIsConnActive:
		_, exists := g.connections[c.id]
		c.reply <- exists

	case *cmdGetConnByLeaseEntry:
		conn, exists := g.connections[c.connID]
		if exists {
			c.reply <- conn
		} else {
			c.reply <- nil
		}

	case *cmdRegisterLeaseConn:
		g.leaseConnections[c.leaseID] = c.conn
		c.done <- struct{}{}

	case *cmdUnregisterLeaseConn:
		delete(g.leaseConnections, c.leaseID)
		c.done <- struct{}{}

	case *cmdAddRelayed:
		g.relayedConnections[c.leaseID] = append(g.relayedConnections[c.leaseID], c.stream)
		c.done <- struct{}{}

	case *cmdRemoveRelayed:
		if streams, ok := g.relayedConnections[c.leaseID]; ok {
			for i, s := range streams {
				if s == c.stream {
					g.relayedConnections[c.leaseID] = append(streams[:i], streams[i+1:]...)
					break
				}
			}
		}
		c.done <- struct{}{}

	case *cmdCheckAndIncLimit:
		if g.maxRelayedPerLease > 0 && g.relayedPerLeaseCount[c.leaseID] >= g.maxRelayedPerLease {
			c.reply <- false
		} else {
			g.relayedPerLeaseCount[c.leaseID]++
			c.reply <- true
		}

	case *cmdDecLimit:
		if g.relayedPerLeaseCount[c.leaseID] > 0 {
			g.relayedPerLeaseCount[c.leaseID]--
		}
		c.done <- struct{}{}

	case *cmdSetMaxRelayed:
		g.maxRelayedPerLease = c.max
		c.done <- struct{}{}
	}
}

// Traffic control setters
func (g *RelayServer) SetMaxRelayedPerLease(n int) {
	done := make(chan struct{}, 1)
	g.cmdCh <- &cmdSetMaxRelayed{max: n, done: done}
	<-done
}

// SetEstablishRelayCallback sets the callback for relay connection establishment
// This allows external code (e.g., relay-server) to handle BPS limiting
func (g *RelayServer) SetEstablishRelayCallback(
	callback func(clientStream, leaseStream *yamux.Stream, leaseID string),
) {
	g.onEstablishRelay = callback
}
