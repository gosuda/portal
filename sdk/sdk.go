package sdk

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/webtransport-go"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
	utils "gosuda.org/portal/utils"
)

func NewCredential() *cryptoops.Credential {
	cred, err := cryptoops.NewCredential()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create credential")
	}
	return cred
}

func newWebTransportDialer(tlsConfig *tls.Config) func(context.Context, string) (portal.Session, error) {
	return func(ctx context.Context, url string) (portal.Session, error) {
		var dialer webtransport.Dialer
		dialer.TLSClientConfig = tlsConfig
		resp, sess, err := dialer.Dial(ctx, url, nil)
		if resp != nil {
			defer func() {
				if closeErr := resp.Body.Close(); closeErr != nil {
					log.Debug().Err(closeErr).Msg("[SDK] Failed to close WebTransport dial response body")
				}
			}()
		}
		if err != nil {
			return nil, fmt.Errorf("webtransport dial %s: %w", url, err)
		}
		return portal.NewWTSession(sess), nil
	}
}

type Client struct {
	config *ClientConfig
	mu     sync.Mutex

	relays    map[string]*connRelay
	listeners map[string]*Listener

	stopch    chan struct{}
	stopOnce  sync.Once      // Ensure stopch is closed only once
	waitGroup sync.WaitGroup // Track all listener workers
}

func NewClient(opt ...ClientOption) (*Client, error) {
	log.Debug().Msg("[SDK] Creating new Client")

	config := &ClientConfig{
		// Dialer intentionally nil — created after options are applied
		HealthCheckInterval: 10 * time.Second,
		ReconnectMaxRetries: 0,
		ReconnectInterval:   5 * time.Second,
	}

	for _, o := range opt {
		o(config)
	}

	// Create default dialer if none was provided
	if config.Dialer == nil {
		config.Dialer = newWebTransportDialer(config.TLSConfig)
	}

	client := &Client{
		relays:    make(map[string]*connRelay),
		listeners: make(map[string]*Listener),
		config:    config,
		stopch:    make(chan struct{}),
	}

	// Initialize relays from bootstrap servers
	var connectionErrors []error
	for _, server := range config.BootstrapServers {
		normalized, err := utils.NormalizePortalURL(server)
		if err != nil {
			log.Error().
				Err(err).
				Str("server", server).
				Msg("[SDK] Invalid bootstrap server")
			connectionErrors = append(connectionErrors, fmt.Errorf("invalid bootstrap server %q: %w", server, err))
			continue
		}

		err = client.AddRelay(normalized, config.Dialer)
		if err != nil {
			log.Error().
				Err(err).
				Str("server", normalized).
				Msg("[SDK] Failed to connect to bootstrap server")
			connectionErrors = append(connectionErrors, fmt.Errorf("connect bootstrap server %q: %w", normalized, err))
			continue
		}
		log.Debug().
			Str("server_raw", server).
			Str("server", normalized).
			Msg("[SDK] Successfully connected to bootstrap server")
	}

	// If no relays were successfully connected, return an error
	if len(client.relays) == 0 && len(config.BootstrapServers) > 0 {
		log.Error().Int("attempted", len(config.BootstrapServers)).Msg("[SDK] Failed to connect to any bootstrap servers")
		return nil, fmt.Errorf("failed to connect to any bootstrap servers: %w", errors.Join(connectionErrors...))
	}

	log.Debug().Int("relay_count", len(client.relays)).Msg("[SDK] Client created successfully")
	return client, nil
}

func (g *Client) Dial(cred *cryptoops.Credential, leaseID, alpn string) (*Connection, error) {
	log.Debug().
		Str("lease_id", leaseID).
		Str("alpn", alpn).
		Msg("[SDK] Dialing to lease")

	g.mu.Lock()
	relays := make([]*connRelay, 0, len(g.relays))
	for _, server := range g.relays {
		relays = append(relays, server)
	}
	g.mu.Unlock()

	log.Debug().Int("relay_count", len(relays)).Msg("[SDK] Checking relays for lease")

	var wg sync.WaitGroup
	var availableRelaysMu sync.Mutex
	var availableRelays []*connRelay

	for _, relay := range relays {
		wg.Add(1)
		go func(relay *connRelay) {
			defer wg.Done()
			info, err := relay.client.GetRelayInfo()
			if err != nil {
				log.Debug().Err(err).Str("relay", relay.addr).Msg("[SDK] Failed to get relay info")
				return
			}

			for _, lease := range info.Leases {
				if lease.Identity.Id != leaseID {
					continue
				}
				log.Debug().Str("relay", relay.addr).Str("lease_id", leaseID).Msg("[SDK] Found lease on relay")
				availableRelaysMu.Lock()
				availableRelays = append(availableRelays, relay)
				availableRelaysMu.Unlock()
				break
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
		return &Connection{via: relay, conn: conn, localAddr: conn.LocalID(), remoteAddr: conn.RemoteID()}, nil
	}

	log.Warn().Str("lease_id", leaseID).Msg("[SDK] All connection attempts failed")
	return nil, ErrNoAvailableRelay
}

func (g *Client) Listen(cred *cryptoops.Credential, name string, alpns []string, options ...MetadataOption) (*Listener, error) {
	log.Debug().
		Str("lease_id", cred.ID()).
		Str("name", name).
		Strs("alpns", alpns).
		Msg("[SDK] Creating listener")

	// Validate name is URL-safe
	if !utils.IsURLSafeName(name) {
		log.Error().
			Str("name", name).
			Msg("[SDK] Lease name contains invalid characters")
		return nil, ErrInvalidName
	}

	var metadata Metadata
	for _, option := range options {
		option(&metadata)
	}

	var metadataValue string
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		log.Warn().Err(err).Msg("[SDK] Failed to marshal metadata")
	} else {
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
	listener := &Listener{
		cred:   cred,
		lease:  lease,
		conns:  make(map[*Connection]struct{}),
		connCh: make(chan *Connection, 100),
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
		go func(r *connRelay) {
			registerErr := r.client.RegisterLease(cred, listener.lease)
			if registerErr != nil {
				log.Error().Err(registerErr).Str("relay", r.addr).Msg("[SDK] Failed to register lease")
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

func (g *Client) listenerWorker(server *connRelay) {
	defer g.waitGroup.Done()
	log.Debug().Str("relay", server.addr).Msg("[SDK] Listener worker started")

	for {
		select {
		case <-server.stop:
			log.Debug().Str("relay", server.addr).Msg("[SDK] Listener worker stopped")
			return
		case incoming, ok := <-server.client.IncomingConnection():
			if !ok {
				log.Debug().Str("relay", server.addr).Msg("[SDK] Incoming connection channel closed")
				return // Channel closed
			}

			lease := incoming.LeaseID()
			log.Debug().
				Str("relay", server.addr).
				Str("lease_id", lease).
				Str("local", incoming.LocalID()).
				Str("remote", incoming.RemoteID()).
				Msg("[SDK] Received incoming connection")

			g.mu.Lock()
			listener, exists := g.listeners[lease]
			g.mu.Unlock()

			if !exists {
				log.Warn().Str("lease_id", lease).Msg("[SDK] No listener found for lease, closing connection")
				if closeErr := incoming.Close(); closeErr != nil {
					log.Error().Err(closeErr).Str("lease_id", lease).Msg("[SDK] Failed to close unused incoming connection")
				}
				continue
			}

			conn := &Connection{
				via:        server,
				conn:       incoming.SecureConnection,
				localAddr:  incoming.LocalID(),
				remoteAddr: incoming.RemoteID(),
			}

			listener.mu.Lock()
			// Check if listener is still active
			if listener.closed {
				log.Debug().Str("lease_id", lease).Msg("[SDK] Listener closed, rejecting connection")
				listener.mu.Unlock()
				if closeErr := conn.Close(); closeErr != nil {
					log.Error().Err(closeErr).Str("lease_id", lease).Msg("[SDK] Failed to close connection for closed listener")
				}
				continue
			}
			listener.conns[conn] = struct{}{}
			listener.mu.Unlock()

			// Send connection to listener (non-blocking)
			select {
			case listener.connCh <- conn:
				log.Debug().Str("lease_id", lease).Msg("[SDK] Connection sent to listener channel")
				// Connection sent successfully
			default:
				// Channel full, close connection
				log.Warn().Str("lease_id", lease).Msg("[SDK] Listener channel full, closing connection")
				listener.mu.Lock()
				delete(listener.conns, conn)
				listener.mu.Unlock()
				if closeErr := conn.Close(); closeErr != nil {
					log.Error().Err(closeErr).Str("lease_id", lease).Msg("[SDK] Failed to close connection after channel overflow")
				}
			}
		}
	}
}

func (g *Client) Close() error {
	log.Debug().Msg("[SDK] Closing Client")
	var errs []error

	// Signal all goroutines to stop (only once)
	g.stopOnce.Do(func() {
		close(g.stopch)
	})

	g.mu.Lock()
	listeners := make([]*Listener, 0, len(g.listeners))
	for _, listener := range g.listeners {
		listeners = append(listeners, listener)
	}
	relays := make([]*connRelay, 0, len(g.relays))
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
		if err := g.RemoveRelay(relay.addr); err != nil && !errors.Is(err, ErrRelayNotFound) {
			log.Error().Err(err).Str("relay", relay.addr).Msg("[SDK] Error removing relay")
			errs = append(errs, err)
		}
	}

	// Wait for all listener workers to finish
	log.Debug().Msg("[SDK] Waiting for all workers to finish")
	g.waitGroup.Wait()

	log.Debug().Msg("[SDK] Client closed successfully")
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// healthCheckWorker periodically checks relay health and reconnects if needed.
func (g *Client) healthCheckWorker(relay *connRelay) {
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

// reconnectRelay attempts to reconnect to a relay server.
func (g *Client) reconnectRelay(relay *connRelay) {
	addr := relay.addr
	dialer := relay.dialer

	log.Debug().Str("relay", addr).Msg("[SDK] Starting reconnection process")

	// Remove the failed relay
	if err := g.RemoveRelay(addr); err != nil && !errors.Is(err, ErrRelayNotFound) {
		log.Error().Err(err).Str("relay", addr).Msg("[SDK] Error removing relay during reconnection")
	}

	// Start reconnection in a goroutine
	g.waitGroup.Go(func() {
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

			if errors.Is(err, ErrRelayExists) {
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
	})
}

// AddRelay adds a new relay server to the client.
func (g *Client) AddRelay(addr string, dialer func(context.Context, string) (portal.Session, error)) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Check if relay already exists
	if _, exists := g.relays[addr]; exists {
		return ErrRelayExists
	}

	// Connect to relay
	sess, err := dialer(context.Background(), addr)
	if err != nil {
		return err
	}

	relayClient := portal.NewRelayClient(sess)

	// Add relay
	relay := &connRelay{
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
			registerErr := relayClient.RegisterLease(cred, lease)
			if registerErr != nil {
				log.Error().
					Err(registerErr).
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

// RemoveRelay removes a relay server from the client.
func (g *Client) RemoveRelay(addr string) error {
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

// GetRelays returns a list of all relay addresses.
func (g *Client) GetRelays() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	relays := make([]string, 0, len(g.relays))
	for addr := range g.relays {
		relays = append(relays, addr)
	}

	return relays
}

func (g *Client) LookupName(name string) (*rdverb.Lease, error) {
	log.Debug().Str("name", name).Msg("[SDK] Looking up name")

	g.mu.Lock()
	relays := make([]*connRelay, 0, len(g.relays))
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

type Listener struct {
	mu sync.Mutex

	cred  *cryptoops.Credential
	lease *rdverb.Lease

	conns map[*Connection]struct{}

	connCh chan *Connection
	closed bool
}

// Implement net.Listener interface for Listener.
func (l *Listener) Accept() (net.Conn, error) {
	conn, ok := <-l.connCh
	if !ok {
		return nil, net.ErrClosed
	}
	return conn, nil
}

func (l *Listener) Close() error {
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
	l.conns = make(map[*Connection]struct{})

	return nil
}

func (l *Listener) Addr() net.Addr {
	return addr(l.cred.ID())
}

type connRelay struct {
	addr     string
	client   *portal.RelayClient
	dialer   func(context.Context, string) (portal.Session, error)
	stop     chan struct{}
	stopOnce sync.Once // Ensure stop channel is closed only once
	mu       sync.Mutex
}

var _ net.Conn = (*Connection)(nil)

type Connection struct {
	via        *connRelay
	localAddr  string
	remoteAddr string
	conn       *cryptoops.SecureConnection
}

func (r *Connection) Read(b []byte) (n int, err error) {
	return r.conn.Read(b)
}

func (r *Connection) Write(b []byte) (n int, err error) {
	return r.conn.Write(b)
}

func (r *Connection) Close() error {
	return r.conn.Close()
}

func (r *Connection) LocalAddr() net.Addr {
	return addr(r.localAddr)
}

func (r *Connection) RemoteAddr() net.Addr {
	return addr(r.remoteAddr)
}

func (r *Connection) SetDeadline(t time.Time) error {
	return r.conn.SetDeadline(t)
}

func (r *Connection) SetReadDeadline(t time.Time) error {
	return r.conn.SetReadDeadline(t)
}

func (r *Connection) SetWriteDeadline(t time.Time) error {
	return r.conn.SetWriteDeadline(t)
}

var _ net.Addr = (*addr)(nil)

type addr string

func (a addr) Network() string {
	return "portal"
}

func (a addr) String() string {
	return string(a)
}
