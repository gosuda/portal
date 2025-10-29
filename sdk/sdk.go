package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/gosuda/relaydns/relaydns"
	"github.com/gosuda/relaydns/relaydns/core/cryptoops"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdverb"
	"github.com/gosuda/relaydns/relaydns/utils/wsstream"
	"github.com/rs/zerolog/log"
)

func NewCredential() *cryptoops.Credential {
	cred, err := cryptoops.NewCredential()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create credential")
	}
	return cred
}

// URL-safe name validation regex
// Allows: Unicode letters (\p{L}), Unicode numbers (\p{N}), hyphen (-), underscore (_)
// This includes Korean (한글), Japanese (日本語), Chinese (中文), Arabic (العربية), etc.
var urlSafeNameRegex = regexp.MustCompile(`^[\p{L}\p{N}_-]+$`)

// isURLSafeName checks if a name contains only URL-safe characters
// Supports Unicode characters including Korean (한글), Japanese (日本語), Chinese (中文), etc.
// Disallows: spaces, special characters like /, ?, &, =, %, etc.
// Note: Browsers will automatically URL-encode non-ASCII characters (e.g., 한글 → %ED%95%9C%EA%B8%80)
func isURLSafeName(name string) bool {
	if name == "" {
		return true // Empty name is allowed (will be treated as unnamed)
	}
	return urlSafeNameRegex.MatchString(name)
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
	BootstrapServers    []string
	Dialer              func(context.Context, string) (io.ReadWriteCloser, error)
	HealthCheckInterval time.Duration // Interval for health checks (default: 10 seconds)
	ReconnectMaxRetries int           // Maximum reconnection attempts (default: 3, 0 = infinite)
	ReconnectInterval   time.Duration // Interval between reconnection attempts (default: 5 seconds)
}

type Option func(*RDClientConfig)

func WithBootstrapServers(servers []string) Option {
	return func(c *RDClientConfig) {
		c.BootstrapServers = servers
	}
}

func WithDialer(dialer func(context.Context, string) (io.ReadWriteCloser, error)) Option {
	return func(c *RDClientConfig) {
		c.Dialer = dialer
	}
}

func WithHealthCheckInterval(interval time.Duration) Option {
	return func(c *RDClientConfig) {
		c.HealthCheckInterval = interval
	}
}

func WithReconnectMaxRetries(retries int) Option {
	return func(c *RDClientConfig) {
		c.ReconnectMaxRetries = retries
	}
}

func WithReconnectInterval(interval time.Duration) Option {
	return func(c *RDClientConfig) {
		c.ReconnectInterval = interval
	}
}

type rdRelay struct {
	addr   string
	client *relaydns.RelayClient
	dialer func(context.Context, string) (io.ReadWriteCloser, error)
	stop   chan struct{}
	mu     sync.Mutex
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
	config    *RDClientConfig

	stopch    chan struct{}
	waitGroup sync.WaitGroup // Track all listener workers
}

var (
	ErrNoAvailableRelay     = errors.New("no available relay")
	ErrClientClosed         = errors.New("client is closed")
	ErrListenerExists       = errors.New("listener already exists for this credential")
	ErrRelayExists          = errors.New("relay already exists")
	ErrRelayNotFound        = errors.New("relay not found")
	ErrInvalidName          = errors.New("lease name contains invalid characters (only alphanumeric, hyphen, underscore allowed)")
	ErrFailedToCreateClient = errors.New("failed to create relay client")
)

func NewClient(opt ...Option) (*RDClient, error) {
	log.Debug().Msg("[SDK] Creating new RDClient")

	config := &RDClientConfig{
		Dialer:              webSocketDialer(),
		HealthCheckInterval: 10 * time.Second,
		ReconnectMaxRetries: 9,
		ReconnectInterval:   5 * time.Second,
	}

	for _, o := range opt {
		o(config)
	}

	client := &RDClient{
		relays:    make(map[string]*rdRelay),
		listeners: make(map[string]*RDListener),
		config:    config,
		stopch:    make(chan struct{}),
	}

	// Initialize relays from bootstrap servers
	var connectionErrors []error
	for _, server := range config.BootstrapServers {
		log.Debug().Str("server", server).Msg("[SDK] Connecting to bootstrap server")
		conn, err := config.Dialer(context.Background(), server)
		if err != nil {
			log.Error().Err(err).Str("server", server).Msg("[SDK] Failed to connect to bootstrap server")
			connectionErrors = append(connectionErrors, err)
			continue // Skip failed connections
		}

		relayClient := relaydns.NewRelayClient(conn)
		if relayClient == nil {
			log.Error().Str("server", server).Msg("[SDK] Failed to create relay client")
			conn.Close()
			connectionErrors = append(connectionErrors, ErrFailedToCreateClient)
			continue
		}

		log.Debug().Str("server", server).Msg("[SDK] Successfully connected to bootstrap server")
		relay := &rdRelay{
			addr:   server,
			client: relayClient,
			dialer: config.Dialer,
			stop:   make(chan struct{}),
		}
		client.relays[server] = relay

		// Start health monitoring for this relay
		client.waitGroup.Add(1)
		go client.healthCheckWorker(relay, config)
	}

	// If no relays were successfully connected, return an error
	if len(client.relays) == 0 && len(config.BootstrapServers) > 0 {
		log.Error().Int("attempted", len(config.BootstrapServers)).Msg("[SDK] Failed to connect to any bootstrap servers")
		return nil, fmt.Errorf("failed to connect to any bootstrap servers: %v", connectionErrors)
	}

	log.Debug().Int("relay_count", len(client.relays)).Msg("[SDK] RDClient created successfully")
	return client, nil
}

func (g *RDClient) Dial(cred *cryptoops.Credential, leaseID string, alpn string) (*RDConnection, error) {
	log.Debug().
		Str("lease_id", leaseID).
		Str("alpn", alpn).
		Msg("[SDK] Dialing to lease")

	var relays []*rdRelay

	g.mu.Lock()
	for _, server := range g.relays {
		relays = append(relays, server)
	}
	g.mu.Unlock()

	log.Debug().Int("relay_count", len(relays)).Msg("[SDK] Checking relays for lease")

	var wg sync.WaitGroup
	var availableRelaysMu sync.Mutex
	var availableRelays []*rdRelay

	for _, relay := range relays {
		wg.Add(1)
		go func(relay *rdRelay) {
			defer wg.Done()
			info, err := relay.client.GetRelayInfo()
			if err != nil {
				log.Debug().Err(err).Str("relay", relay.addr).Msg("[SDK] Failed to get relay info")
				return
			}

			if slices.Contains(info.Leases, leaseID) {
				log.Debug().Str("relay", relay.addr).Str("lease_id", leaseID).Msg("[SDK] Found lease on relay")
				availableRelaysMu.Lock()
				availableRelays = append(availableRelays, relay)
				availableRelaysMu.Unlock()
			}
		}(relay)
	}
	wg.Wait()

	if len(availableRelays) == 0 {
		log.Warn().Str("lease_id", leaseID).Msg("[SDK] No available relay found for lease")
		return nil, ErrNoAvailableRelay
	}

	log.Debug().Int("available_relays", len(availableRelays)).Str("lease_id", leaseID).Msg("[SDK] Attempting to connect")

	for _, relay := range availableRelays {
		log.Debug().Str("relay", relay.addr).Str("lease_id", leaseID).Msg("[SDK] Requesting connection")
		code, conn, err := relay.client.RequestConnection(leaseID, alpn, cred)
		if err != nil || code != rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
			log.Debug().
				Err(err).
				Str("relay", relay.addr).
				Str("code", code.String()).
				Msg("[SDK] Connection request failed, trying next relay")
			continue
		}
		log.Debug().
			Str("relay", relay.addr).
			Str("lease_id", leaseID).
			Str("local", conn.LocalID()).
			Str("remote", conn.RemoteID()).
			Msg("[SDK] Connection established successfully")
		return &RDConnection{via: relay, conn: conn, localAddr: conn.LocalID(), remoteAddr: conn.RemoteID()}, nil
	}

	log.Warn().Str("lease_id", leaseID).Msg("[SDK] All connection attempts failed")
	return nil, ErrNoAvailableRelay
}

func (g *RDClient) Listen(cred *cryptoops.Credential, name string, alpns []string) (*RDListener, error) {
	log.Debug().
		Str("lease_id", cred.ID()).
		Str("name", name).
		Strs("alpns", alpns).
		Msg("[SDK] Creating listener")

	// Validate name is URL-safe
	if !isURLSafeName(name) {
		log.Error().
			Str("name", name).
			Msg("[SDK] Lease name contains invalid characters")
		return nil, ErrInvalidName
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	// Check if client is closed
	select {
	case <-g.stopch:
		log.Error().Msg("[SDK] Cannot create listener, client is closed")
		return nil, ErrClientClosed
	default:
		// Client is still open
	}

	// Check if listener already exists
	if _, exists := g.listeners[cred.ID()]; exists {
		log.Warn().Str("lease_id", cred.ID()).Msg("[SDK] Listener already exists")
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

	log.Debug().
		Str("lease_id", cred.ID()).
		Int("relay_count", len(g.relays)).
		Msg("[SDK] Registering lease with relays")

	// Register lease with all available relays
	for _, relay := range g.relays {
		go func(r *rdRelay) {
			err := r.client.RegisterLease(cred, name, alpns)
			if err != nil {
				log.Error().Err(err).Str("relay", r.addr).Msg("[SDK] Failed to register lease")
			} else {
				log.Debug().Str("relay", r.addr).Msg("[SDK] Lease registered successfully")
			}
		}(relay)
	}

	// Start listener worker for each relay
	for _, relay := range g.relays {
		g.waitGroup.Add(1)
		go g.listenerWorker(relay)
	}

	log.Debug().Str("lease_id", cred.ID()).Msg("[SDK] Listener created successfully")
	return listener, nil
}

func (g *RDClient) listenerWorker(server *rdRelay) {
	defer g.waitGroup.Done()
	log.Debug().Str("relay", server.addr).Msg("[SDK] Listener worker started")

	for {
		select {
		case <-server.stop:
			log.Debug().Str("relay", server.addr).Msg("[SDK] Listener worker stopped")
			return
		case conn, ok := <-server.client.IncommingConnection():
			if !ok {
				log.Debug().Str("relay", server.addr).Msg("[SDK] Incoming connection channel closed")
				return // Channel closed
			}

			lease := conn.LeaseID()
			log.Debug().
				Str("relay", server.addr).
				Str("lease_id", lease).
				Str("local", conn.LocalID()).
				Str("remote", conn.RemoteID()).
				Msg("[SDK] Received incoming connection")

			g.mu.Lock()
			listener, exists := g.listeners[lease]
			g.mu.Unlock()

			if !exists {
				log.Warn().Str("lease_id", lease).Msg("[SDK] No listener found for lease, closing connection")
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
				log.Debug().Str("lease_id", lease).Msg("[SDK] Listener closed, rejecting connection")
				listener.mu.Unlock()
				rdConn.Close()
				continue
			}
			listener.conns[rdConn] = struct{}{}
			listener.mu.Unlock()

			// Send connection to listener (non-blocking)
			select {
			case listener.connCh <- rdConn:
				log.Debug().Str("lease_id", lease).Msg("[SDK] Connection sent to listener channel")
				// Connection sent successfully
			default:
				// Channel full, close connection
				log.Warn().Str("lease_id", lease).Msg("[SDK] Listener channel full, closing connection")
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

	// Wait for all listener workers to finish
	g.waitGroup.Wait()

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// healthCheckWorker periodically checks relay health and reconnects if needed
func (g *RDClient) healthCheckWorker(relay *rdRelay, config *RDClientConfig) {
	defer g.waitGroup.Done()

	ticker := time.NewTicker(config.HealthCheckInterval)
	defer ticker.Stop()

	log.Debug().Str("relay", relay.addr).Msg("[SDK] Health check worker started")

	for {
		select {
		case <-g.stopch:
			log.Debug().Str("relay", relay.addr).Msg("[SDK] Health check worker stopped")
			return
		case <-relay.stop:
			log.Debug().Str("relay", relay.addr).Msg("[SDK] Relay stopped, health check worker exiting")
			return
		case <-ticker.C:
			relay.mu.Lock()
			client := relay.client
			relay.mu.Unlock()

			if client == nil {
				log.Warn().Str("relay", relay.addr).Msg("[SDK] Relay client is nil, attempting reconnection")
				g.reconnectRelay(relay, config)
				continue
			}

			// Perform health check using Ping
			_, err := client.Ping()
			if err != nil {
				log.Warn().
					Err(err).
					Str("relay", relay.addr).
					Msg("[SDK] Health check failed, attempting reconnection")
				g.reconnectRelay(relay, config)
			} else {
				log.Debug().Str("relay", relay.addr).Msg("[SDK] Health check passed")
			}
		}
	}
}

// reconnectRelay attempts to reconnect to a relay server
func (g *RDClient) reconnectRelay(relay *rdRelay, config *RDClientConfig) {
	relay.mu.Lock()

	// Close old client if exists
	if relay.client != nil {
		log.Debug().Str("relay", relay.addr).Msg("[SDK] Closing old relay client")
		relay.client.Close()
		relay.client = nil
	}
	relay.mu.Unlock()

	maxRetries := config.ReconnectMaxRetries
	if maxRetries == 0 {
		maxRetries = -1 // Infinite retries
	}

	attempt := 0
	for {
		// Check if we should stop
		select {
		case <-g.stopch:
			log.Debug().Str("relay", relay.addr).Msg("[SDK] Client stopped, abandoning reconnection")
			return
		case <-relay.stop:
			log.Debug().Str("relay", relay.addr).Msg("[SDK] Relay stopped, abandoning reconnection")
			return
		default:
		}

		attempt++
		if maxRetries > 0 && attempt > maxRetries {
			log.Error().
				Str("relay", relay.addr).
				Int("attempts", attempt-1).
				Msg("[SDK] Max reconnection attempts reached, giving up")
			return
		}

		log.Debug().
			Str("relay", relay.addr).
			Int("attempt", attempt).
			Msg("[SDK] Attempting to reconnect")

		// Attempt to connect
		conn, err := relay.dialer(context.Background(), relay.addr)
		if err != nil {
			log.Warn().
				Err(err).
				Str("relay", relay.addr).
				Int("attempt", attempt).
				Msg("[SDK] Reconnection attempt failed")

			// Wait before next retry
			select {
			case <-g.stopch:
				return
			case <-relay.stop:
				return
			case <-time.After(config.ReconnectInterval):
				continue
			}
		}

		// Create new relay client
		relayClient := relaydns.NewRelayClient(conn)
		if relayClient == nil {
			log.Error().Str("relay", relay.addr).Msg("[SDK] Failed to create relay client after reconnection")
			conn.Close()

			// Wait before next retry
			select {
			case <-g.stopch:
				return
			case <-relay.stop:
				return
			case <-time.After(config.ReconnectInterval):
				continue
			}
		}

		relay.mu.Lock()
		relay.client = relayClient
		relay.mu.Unlock()

		log.Info().
			Str("relay", relay.addr).
			Int("attempt", attempt).
			Msg("[SDK] Successfully reconnected to relay")

		// Re-register all leases with the reconnected relay
		g.mu.Lock()
		for _, listener := range g.listeners {
			go func(l *RDListener) {
				l.mu.Lock()
				cred := l.cred
				lease := l.lease
				l.mu.Unlock()

				if lease != nil {
					err := relayClient.RegisterLease(cred, lease.Name, lease.Alpn)
					if err != nil {
						log.Error().
							Err(err).
							Str("relay", relay.addr).
							Str("lease_id", cred.ID()).
							Msg("[SDK] Failed to re-register lease after reconnection")
					} else {
						log.Debug().
							Str("relay", relay.addr).
							Str("lease_id", cred.ID()).
							Msg("[SDK] Lease re-registered after reconnection")
					}
				}
			}(listener)
		}
		g.mu.Unlock()

		return
	}
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
	relay := &rdRelay{
		addr:   addr,
		client: relayClient,
		dialer: dialer,
		stop:   make(chan struct{}),
	}
	g.relays[addr] = relay

	// Start health monitoring for this relay
	g.waitGroup.Add(1)
	go g.healthCheckWorker(relay, g.config)

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
