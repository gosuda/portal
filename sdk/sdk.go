package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/gosuda/relaydns/relaydns"
	"github.com/gosuda/relaydns/relaydns/core/cryptoops"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdverb"
	"github.com/gosuda/relaydns/relaydns/utils/wsstream"
)

func NewCredential() (*cryptoops.Credential, error) {
	return cryptoops.NewCredential()
}

func webSocketDialer() func(context.Context, string) (io.ReadWriteCloser, error) {
	return func(ctx context.Context, url string) (io.ReadWriteCloser, error) {
		wsConn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			return nil, err
		}
		return &wsstream.WsStream{Conn: wsConn}, nil
	}
}

type RDClientConfig struct {
	BootstrapServers []string
	Dialer           func(context.Context, string) (io.ReadWriteCloser, error)
}

type Option func(*RDClientConfig)

type rdRelay struct {
	addr   string
	client *relaydns.RelayClient
	stop   chan struct{}
}

var _ net.Conn = (*RDConnection)(nil)

type RDConnection struct {
	via        *rdRelay
	localAddr  string
	remoteAddr string
	conn       *cryptoops.SecureConnection
}

// Implement net.Conn interface for RDConnection
func (r *RDConnection) Read(b []byte) (n int, err error) {
	return r.conn.Read(b)
}

func (r *RDConnection) Write(b []byte) (n int, err error) {
	return r.conn.Write(b)
}

func (r *RDConnection) Close() error {
	return r.conn.Close()
}

func (r *RDConnection) LocalAddr() net.Addr {
	return rdAddr(r.localAddr)
}

func (r *RDConnection) RemoteAddr() net.Addr {
	return rdAddr(r.remoteAddr)
}

func (r *RDConnection) SetDeadline(t time.Time) error {
	return r.conn.SetDeadline(t)
}

func (r *RDConnection) SetReadDeadline(t time.Time) error {
	return r.conn.SetReadDeadline(t)
}

func (r *RDConnection) SetWriteDeadline(t time.Time) error {
	return r.conn.SetWriteDeadline(t)
}

// rdAddr implements net.Addr
type rdAddr string

func (a rdAddr) Network() string {
	return "relaydns"
}

func (a rdAddr) String() string {
	return string(a)
}

type RDListener struct {
	mu sync.Mutex

	cred  *cryptoops.Credential
	lease *rdverb.Lease

	conns map[*RDConnection]struct{}

	connCh chan *RDConnection
	closed bool
}

type RDClient struct {
	mu sync.Mutex

	relays    map[string]*rdRelay
	listeners map[string]*RDListener

	stopch chan struct{}
}

var (
	ErrNoAvailableRelay     = errors.New("no available relay")
	ErrClientClosed         = errors.New("client is closed")
	ErrListenerExists       = errors.New("listener already exists for this credential")
	ErrRelayExists          = errors.New("relay already exists")
	ErrRelayNotFound        = errors.New("relay not found")
	ErrFailedToCreateClient = errors.New("failed to create relay client")
)

func NewClient(opt ...Option) (*RDClient, error) {
	config := &RDClientConfig{
		Dialer: webSocketDialer(),
	}

	for _, o := range opt {
		o(config)
	}

	client := &RDClient{
		relays:    make(map[string]*rdRelay),
		listeners: make(map[string]*RDListener),
		stopch:    make(chan struct{}),
	}

	// Initialize relays from bootstrap servers
	var connectionErrors []error
	for _, server := range config.BootstrapServers {
		conn, err := config.Dialer(context.Background(), server)
		if err != nil {
			connectionErrors = append(connectionErrors, err)
			continue // Skip failed connections
		}

		relayClient := relaydns.NewRelayClient(conn)
		if relayClient == nil {
			conn.Close()
			connectionErrors = append(connectionErrors, ErrFailedToCreateClient)
			continue
		}

		client.relays[server] = &rdRelay{
			addr:   server,
			client: relayClient,
			stop:   make(chan struct{}),
		}
	}

	// If no relays were successfully connected, return an error
	if len(client.relays) == 0 && len(config.BootstrapServers) > 0 {
		return nil, fmt.Errorf("failed to connect to any bootstrap servers: %v", connectionErrors)
	}

	return client, nil
}

func (g *RDClient) Dial(cred *cryptoops.Credential, leaseID string, alpn string) (*RDConnection, error) {
	var relays []*rdRelay

	g.mu.Lock()
	for _, server := range g.relays {
		relays = append(relays, server)
	}
	g.mu.Unlock()

	var wg sync.WaitGroup
	var availableRelaysMu sync.Mutex
	var availableRelays []*rdRelay

	for _, relay := range relays {
		wg.Add(1)
		go func(relay *rdRelay) {
			defer wg.Done()
			info, err := relay.client.GetRelayInfo()
			if err != nil {
				return
			}

			if slices.Contains(info.Leases, leaseID) {
				availableRelaysMu.Lock()
				availableRelays = append(availableRelays, relay)
				availableRelaysMu.Unlock()
			}
		}(relay)
	}
	wg.Wait()

	if len(availableRelays) == 0 {
		return nil, ErrNoAvailableRelay
	}

	for _, relay := range availableRelays {
		code, conn, err := relay.client.RequestConnection(leaseID, alpn, cred)
		if err != nil || code != rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
			continue
		}
		return &RDConnection{via: relay, conn: conn, localAddr: conn.LocalID(), remoteAddr: conn.RemoteID()}, nil
	}

	return nil, ErrNoAvailableRelay
}

func (g *RDClient) Listen(cred *cryptoops.Credential, name string, alpns []string) (*RDListener, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Check if client is closed
	select {
	case <-g.stopch:
		return nil, ErrClientClosed
	default:
		// Client is still open
	}

	// Check if listener already exists
	if _, exists := g.listeners[cred.ID()]; exists {
		return nil, ErrListenerExists
	}

	// Create listener
	listener := &RDListener{
		cred:   cred,
		conns:  make(map[*RDConnection]struct{}),
		connCh: make(chan *RDConnection, 100),
		closed: false,
	}

	// Register listener
	g.listeners[cred.ID()] = listener

	// Register lease with all available relays
	for _, relay := range g.relays {
		go func(r *rdRelay) {
			r.client.RegisterLease(cred, name, alpns)
		}(relay)
	}

	// Start listener worker for each relay
	for _, relay := range g.relays {
		go g.listenerWorker(relay)
	}

	return listener, nil
}

func (g *RDClient) listenerWorker(server *rdRelay) {
	for {
		select {
		case <-server.stop:
			return
		case conn, ok := <-server.client.IncommingConnection():
			if !ok {
				return // Channel closed
			}

			lease := conn.LeaseID()

			g.mu.Lock()
			listener, exists := g.listeners[lease]
			g.mu.Unlock()

			if !exists {
				conn.SecureConnection.Close() // Close unused connection
				continue
			}

			rdConn := &RDConnection{
				via:        server,
				conn:       conn.SecureConnection,
				localAddr:  conn.LocalID(),
				remoteAddr: conn.RemoteID(),
			}

			listener.mu.Lock()
			// Check if listener is still active
			if listener.closed {
				listener.mu.Unlock()
				rdConn.Close()
				continue
			}
			listener.conns[rdConn] = struct{}{}
			listener.mu.Unlock()

			// Send connection to listener (non-blocking)
			select {
			case listener.connCh <- rdConn:
				// Connection sent successfully
			default:
				// Channel full, close connection
				listener.mu.Lock()
				delete(listener.conns, rdConn)
				listener.mu.Unlock()
				rdConn.Close()
			}
		}
	}
}

func (g *RDClient) Close() error {
	var errs []error

	// Signal all goroutines to stop
	close(g.stopch)

	g.mu.Lock()
	for _, listener := range g.listeners {
		if err := listener.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	g.listeners = make(map[string]*RDListener)

	// Stop all relays
	for _, server := range g.relays {
		close(server.stop) // Signal relay goroutines to stop
		if err := server.client.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	g.relays = make(map[string]*rdRelay)
	g.mu.Unlock()

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// Implement net.Listener interface for RDListener
func (l *RDListener) Accept() (net.Conn, error) {
	conn, ok := <-l.connCh
	if !ok {
		return nil, net.ErrClosed
	}
	return conn, nil
}

func (l *RDListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}

	l.closed = true

	// Close the connection channel first to prevent new connections
	close(l.connCh)

	// Close all active connections
	for conn := range l.conns {
		if err := conn.Close(); err != nil {
			// Log error but continue closing other connections
			// In a real implementation, you might want to collect errors
		}
		delete(l.conns, conn)
	}

	// Clear the connections map
	l.conns = make(map[*RDConnection]struct{})

	return nil
}

func (l *RDListener) Addr() net.Addr {
	return rdAddr(l.cred.ID())
}

// AddRelay adds a new relay server to the client
func (g *RDClient) AddRelay(addr string, dialer func(context.Context, string) (io.ReadWriteCloser, error)) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Check if relay already exists
	if _, exists := g.relays[addr]; exists {
		return errors.New("relay already exists")
	}

	// Connect to relay
	conn, err := dialer(context.Background(), addr)
	if err != nil {
		return err
	}

	// Create relay client
	relayClient := relaydns.NewRelayClient(conn)
	if relayClient == nil {
		conn.Close()
		return errors.New("failed to create relay client")
	}

	// Add relay
	g.relays[addr] = &rdRelay{
		addr:   addr,
		client: relayClient,
		stop:   make(chan struct{}),
	}

	return nil
}

// RemoveRelay removes a relay server from the client
func (g *RDClient) RemoveRelay(addr string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	relay, exists := g.relays[addr]
	if !exists {
		return errors.New("relay not found")
	}

	// Signal relay to stop
	close(relay.stop)

	// Close relay client
	if err := relay.client.Close(); err != nil {
		return err
	}

	// Remove from map
	delete(g.relays, addr)

	return nil
}

// GetRelays returns a list of all relay addresses
func (g *RDClient) GetRelays() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	relays := make([]string, 0, len(g.relays))
	for addr := range g.relays {
		relays = append(relays, addr)
	}

	return relays
}
