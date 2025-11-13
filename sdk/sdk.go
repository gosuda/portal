package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
	"gosuda.org/portal/portal/utils/wsstream"
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
	ReconnectMaxRetries int           // Maximum reconnection attempts (default: 0 = infinite)
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
	addr     string
	client   *portal.RelayClient
	dialer   func(context.Context, string) (io.ReadWriteCloser, error)
	stop     chan struct{}
	stopOnce sync.Once // Ensure stop channel is closed only once
	mu       sync.Mutex
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
	return "portal"
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

type Metadata struct {
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Thumbnail   string   `json:"thumbnail"`
	Owner       string   `json:"owner"`
	Country     string   `json:"country"`
	Hide 				bool     `json:"hide"` // 고수다 숨김 여부
}

func (m Metadata) isEmpty() bool {
	return m.Description == "" &&
		len(m.Tags) == 0 &&
		m.Thumbnail == "" &&
		m.Owner == "" &&
		m.Country == ""
}

type MetadataOption func(*Metadata)

func WithDescription(description string) MetadataOption {
	return func(m *Metadata) {
		m.Description = description
	}
}

func WithTags(tags []string) MetadataOption {
	return func(m *Metadata) {
		m.Tags = tags
	}
}

func WithThumbnail(thumbnail string) MetadataOption {
	return func(m *Metadata) {
		m.Thumbnail = thumbnail
	}
}

func WithOwner(owner string) MetadataOption {
	return func(m *Metadata) {
		m.Owner = owner
	}
}

func WithCountry(country string) MetadataOption {
	return func(m *Metadata) {
		m.Country = country
	}
}

func WithHide(hide bool) MetadataOption {
    return func(m *Metadata) {
        m.Hide = hide
    }
}

type RDClient struct {
	mu sync.Mutex

	relays    map[string]*rdRelay
	listeners map[string]*RDListener
	config    *RDClientConfig

	stopch    chan struct{}
	stopOnce  sync.Once      // Ensure stopch is closed only once
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
	ErrInvalidMetadata      = errors.New("invalid metadata")
)

func NewClient(opt ...Option) (*RDClient, error) {
	log.Debug().Msg("[SDK] Creating new RDClient")

	config := &RDClientConfig{
		Dialer:              webSocketDialer(),
		HealthCheckInterval: 10 * time.Second,
		ReconnectMaxRetries: 0,
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
		err := client.AddRelay(server, config.Dialer)
		if err != nil {
			log.Error().Err(err).Str("server", server).Msg("[SDK] Failed to connect to bootstrap server")
			connectionErrors = append(connectionErrors, err)
		}
		log.Debug().Str("server", server).Msg("[SDK] Successfully connected to bootstrap server")
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

			for _, lease := range info.Leases {
				if lease.Identity.Id == leaseID {
					log.Debug().Str("relay", relay.addr).Str("lease_id", leaseID).Msg("[SDK] Found lease on relay")
					availableRelaysMu.Lock()
					availableRelays = append(availableRelays, relay)
					availableRelaysMu.Unlock()
					break
				}
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

func (g *RDClient) Listen(cred *cryptoops.Credential, name string, alpns []string, options ...MetadataOption) (*RDListener, error) {
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

	var metadata Metadata
	for _, option := range options {
		option(&metadata)
	}

	metadataValue := ""
	if !metadata.isEmpty() {
		metadataJSON, err := json.Marshal(metadata)
		if err != nil {
			log.Error().Err(err).Msg("[SDK] Failed to marshal metadata")
			return nil, ErrInvalidMetadata
		}
		metadataValue = string(metadataJSON)
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

	lease := &rdverb.Lease{
		Identity: &rdsec.Identity{
			Id:        cred.ID(),
			PublicKey: cred.PublicKey(),
		},
		Name:     name,
		Alpn:     alpns,
		Metadata: metadataValue,
	}

	// Create listener with lease metadata for re-registration
	listener := &RDListener{
		cred:   cred,
		lease:  lease,
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
			err := r.client.RegisterLease(cred, listener.lease)
			if err != nil {
				log.Error().Err(err).Str("relay", r.addr).Msg("[SDK] Failed to register lease")
			} else {
				log.Debug().Str("relay", r.addr).Msg("[SDK] Lease registered successfully")
				// Store lease info in listener for future re-registration
				listener.mu.Lock()
				listener.lease = lease
				listener.mu.Unlock()
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
		case conn, ok := <-server.client.IncomingConnection():
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
	log.Debug().Msg("[SDK] Closing RDClient")
	var errs []error

	// Signal all goroutines to stop (only once)
	g.stopOnce.Do(func() {
		close(g.stopch)
	})

	g.mu.Lock()
	listeners := make([]*RDListener, 0, len(g.listeners))
	for _, listener := range g.listeners {
		listeners = append(listeners, listener)
	}
	relays := make([]*rdRelay, 0, len(g.relays))
	for _, relay := range g.relays {
		relays = append(relays, relay)
	}
	g.mu.Unlock()

	// Close all listeners first
	for _, listener := range listeners {
		if err := listener.Close(); err != nil {
			log.Error().Err(err).Msg("[SDK] Error closing listener")
			errs = append(errs, err)
		}
	}

	// Stop all relays
	for _, relay := range relays {
		if err := g.RemoveRelay(relay.addr); err != nil && err != ErrRelayNotFound {
			log.Error().Err(err).Str("relay", relay.addr).Msg("[SDK] Error removing relay")
			errs = append(errs, err)
		}
	}

	// Wait for all listener workers to finish
	log.Debug().Msg("[SDK] Waiting for all workers to finish")
	g.waitGroup.Wait()

	log.Debug().Msg("[SDK] RDClient closed successfully")
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// healthCheckWorker periodically checks relay health and reconnects if needed
func (g *RDClient) healthCheckWorker(relay *rdRelay) {
	defer g.waitGroup.Done()

	ticker := time.NewTicker(g.config.HealthCheckInterval)
	defer ticker.Stop()

	log.Debug().Str("relay", relay.addr).Msg("[SDK] Health check worker started")

	for {
		select {
		case <-g.stopch:
			log.Debug().Str("relay", relay.addr).Msg("[SDK] Health check worker stopped (client closing)")
			return
		case <-relay.stop:
			log.Debug().Str("relay", relay.addr).Msg("[SDK] Health check worker stopped (relay stopped)")
			return
		case <-ticker.C:
			// Check if client is still active
			select {
			case <-g.stopch:
				return
			case <-relay.stop:
				return
			default:
			}

			relay.mu.Lock()
			client := relay.client
			relay.mu.Unlock()

			if client == nil {
				log.Warn().Str("relay", relay.addr).Msg("[SDK] Relay client is nil, attempting reconnection")
				g.reconnectRelay(relay)
				continue
			}

			// Perform health check using Ping
			_, err := client.Ping()
			if err != nil {
				log.Warn().
					Err(err).
					Str("relay", relay.addr).
					Msg("[SDK] Health check failed, attempting reconnection")
				g.reconnectRelay(relay)
				// Continue monitoring instead of returning
				continue
			}
		}
	}
}

// reconnectRelay attempts to reconnect to a relay server
func (g *RDClient) reconnectRelay(relay *rdRelay) {
	addr := relay.addr
	dialer := relay.dialer

	log.Debug().Str("relay", addr).Msg("[SDK] Starting reconnection process")

	// Remove the failed relay
	if err := g.RemoveRelay(addr); err != nil && err != ErrRelayNotFound {
		log.Error().Err(err).Str("relay", addr).Msg("[SDK] Error removing relay during reconnection")
	}

	// Start reconnection in a goroutine
	g.waitGroup.Add(1)
	go func() {
		defer g.waitGroup.Done()

		retries := 0
		maxRetries := g.config.ReconnectMaxRetries

		for {
			// Check if client is shutting down
			select {
			case <-g.stopch:
				log.Debug().Str("relay", addr).Msg("[SDK] Reconnection cancelled (client closing)")
				return
			default:
			}

			// Attempt reconnection
			err := g.AddRelay(addr, dialer)
			if err == nil {
				log.Info().Str("relay", addr).Msg("[SDK] Reconnection successful")
				return
			}

			if err == ErrRelayExists {
				log.Debug().Str("relay", addr).Msg("[SDK] Relay already exists, reconnection complete")
				return
			}

			retries++

			// Check retry limit (0 or negative means infinite retries)
			if maxRetries > 0 && retries >= maxRetries {
				log.Error().
					Err(err).
					Str("relay", addr).
					Int("retries", retries).
					Msg("[SDK] Reconnection failed after max retries")
				return
			}

			log.Warn().
				Err(err).
				Str("relay", addr).
				Int("attempt", retries).
				Msg("[SDK] Reconnection failed, retrying")

			// Wait before next retry with context awareness
			select {
			case <-g.stopch:
				log.Debug().Str("relay", addr).Msg("[SDK] Reconnection cancelled during wait")
				return
			case <-time.After(g.config.ReconnectInterval):
				// Continue to next retry
			}
		}
	}()
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
			log.Error().Err(err).Msg("[SDK] Error closing connection")
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
		return ErrRelayExists
	}

	// Connect to relay
	conn, err := dialer(context.Background(), addr)
	if err != nil {
		return err
	}

	// Create relay client
	relayClient := portal.NewRelayClient(conn)
	if relayClient == nil {
		conn.Close()
		return ErrRelayNotFound
	}

	// Add relay
	relay := &rdRelay{
		addr:   addr,
		client: relayClient,
		dialer: dialer,
		stop:   make(chan struct{}),
	}
	g.relays[addr] = relay

	// Register all existing leases with the new relay
	for _, listener := range g.listeners {
		cred := listener.cred // immutable
		lease := listener.lease.CloneVT()
		go func(cred *cryptoops.Credential, lease *rdverb.Lease) {
			err := relayClient.RegisterLease(cred, lease)
			if err != nil {
				log.Error().
					Err(err).
					Str("relay", addr).
					Str("lease_id", cred.ID()).
					Msg("[SDK] Failed to register lease with new relay")
			} else {
				log.Debug().
					Str("relay", addr).
					Str("lease_id", cred.ID()).
					Msg("[SDK] Lease registered with new relay")
			}
		}(cred, lease)
	}

	// Start listener worker for the new relay
	g.waitGroup.Add(1)
	go g.listenerWorker(relay)

	// Start health monitoring for this relay
	g.waitGroup.Add(1)
	go g.healthCheckWorker(relay)

	log.Info().Str("relay", addr).Msg("[SDK] New relay added successfully")

	return nil
}

// RemoveRelay removes a relay server from the client
func (g *RDClient) RemoveRelay(addr string) error {
	g.mu.Lock()
	relay, exists := g.relays[addr]
	if !exists {
		g.mu.Unlock()
		return ErrRelayNotFound
	}

	// Remove from map immediately to prevent duplicate removals
	delete(g.relays, addr)
	g.mu.Unlock()

	log.Debug().Str("relay", addr).Msg("[SDK] Removing relay")

	// Signal relay to stop (only once)
	relay.stopOnce.Do(func() {
		close(relay.stop)
	})

	// Close relay client
	relay.mu.Lock()
	client := relay.client
	relay.mu.Unlock()

	if client != nil {
		if err := client.Close(); err != nil {
			log.Error().Err(err).Str("relay", addr).Msg("[SDK] Error closing relay client")
			return err
		}
	}

	log.Debug().Str("relay", addr).Msg("[SDK] Relay removed successfully")
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

func (g *RDClient) LookupName(name string) (*rdverb.Lease, error) {
	log.Debug().Str("name", name).Msg("[SDK] Looking up name")
	var relays []*rdRelay

	g.mu.Lock()
	for _, server := range g.relays {
		relays = append(relays, server)
	}
	g.mu.Unlock()

	for _, relay := range relays {
		info, err := relay.client.GetRelayInfo()
		if err != nil {
			log.Error().Err(err).Str("relay", relay.addr).Msg("[SDK] Error getting relay info")
			continue
		}

		for _, lease := range info.Leases {
			if strings.EqualFold(lease.Name, name) {
				log.Debug().Str("name", name).Str("id", lease.Identity.Id).Msg("[SDK] Found lease")
				return lease, nil
			}
		}
	}
	return nil, ErrNoAvailableRelay
}
