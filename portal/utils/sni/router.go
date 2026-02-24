// Package sni provides TLS SNI-based TCP routing for the Portal relay.
package sni

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

var (
	// ErrNoRoute is returned when no route is found for the SNI
	ErrNoRoute = errors.New("no route found for SNI")
	// ErrRouterClosed is returned when the router is closed
	ErrRouterClosed = errors.New("router is closed")
)

const (
	// maxTLSRecordSize is TLS plaintext limit (16KB) plus allowance for overhead.
	// Using this avoids dropping valid large ClientHello messages.
	maxTLSRecordSize = 16*1024 + 2048
)

// Route represents a registered route
type Route struct {
	SNI        string
	TargetAddr string
	LeaseID    string
	LeaseName  string
}

// Router handles SNI-based TCP routing
type Router struct {
	mu       sync.RWMutex
	routes   map[string]*Route // SNI -> Route
	leases   map[string]*Route // LeaseID -> Route
	listener net.Listener

	// Callback for new connections
	onConnection func(conn net.Conn, route *Route)

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewRouter creates a new SNI router
func NewRouter() *Router {
	return &Router{
		routes: make(map[string]*Route),
		leases: make(map[string]*Route),
		stopCh: make(chan struct{}),
	}
}

// SetConnectionCallback sets the callback for new connections
func (r *Router) SetConnectionCallback(cb func(conn net.Conn, route *Route)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onConnection = cb
}

// RegisterRoute registers a new route for an SNI
func (r *Router) RegisterRoute(sni, targetAddr, leaseID, leaseName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	select {
	case <-r.stopCh:
		return ErrRouterClosed
	default:
	}

	sni = strings.ToLower(strings.TrimSpace(sni))
	if sni == "" {
		return fmt.Errorf("sni is required")
	}

	route := &Route{
		SNI:        sni,
		TargetAddr: targetAddr,
		LeaseID:    leaseID,
		LeaseName:  leaseName,
	}

	// Remove previous SNI entry when a lease is re-registered with a new name.
	if oldRoute, ok := r.leases[leaseID]; ok && oldRoute.SNI != sni {
		delete(r.routes, oldRoute.SNI)
	}
	// Keep lease index consistent when SNI is reassigned to another lease.
	if oldRoute, ok := r.routes[sni]; ok && oldRoute.LeaseID != leaseID {
		delete(r.leases, oldRoute.LeaseID)
	}

	r.routes[sni] = route
	r.leases[leaseID] = route

	log.Info().
		Str("sni", sni).
		Str("target", targetAddr).
		Str("lease_id", leaseID).
		Msg("[SNI] Route registered")

	return nil
}

// UnregisterRoute removes a route for an SNI
func (r *Router) UnregisterRoute(sni string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	sni = strings.ToLower(strings.TrimSpace(sni))

	if route, ok := r.routes[sni]; ok {
		delete(r.routes, sni)
		delete(r.leases, route.LeaseID)
		log.Info().
			Str("sni", sni).
			Str("lease_id", route.LeaseID).
			Msg("[SNI] Route unregistered")
	}
}

// UnregisterRouteByLeaseID removes a route by lease ID
func (r *Router) UnregisterRouteByLeaseID(leaseID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if route, ok := r.leases[leaseID]; ok {
		delete(r.routes, route.SNI)
		delete(r.leases, leaseID)
		log.Info().
			Str("sni", route.SNI).
			Str("lease_id", leaseID).
			Msg("[SNI] Route unregistered")
	}
}

// GetRoute returns the route for an SNI
func (r *Router) GetRoute(sni string) (*Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sni = strings.ToLower(strings.TrimSpace(sni))

	// Try exact match first
	if route, ok := r.routes[sni]; ok {
		return route, true
	}

	// Try wildcard match (e.g., *.example.com)
	parts := strings.Split(sni, ".")
	for i := 1; i < len(parts); i++ {
		wildcard := "*." + strings.Join(parts[i:], ".")
		if route, ok := r.routes[wildcard]; ok {
			return route, true
		}
	}

	return nil, false
}

// GetRouteByLeaseID returns the route for a lease ID
func (r *Router) GetRouteByLeaseID(leaseID string) (*Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	route, ok := r.leases[leaseID]
	return route, ok
}

// GetAllRoutes returns all registered routes
func (r *Router) GetAllRoutes() []*Route {
	r.mu.RLock()
	defer r.mu.RUnlock()

	routes := make([]*Route, 0, len(r.routes))
	for _, route := range r.routes {
		routes = append(routes, route)
	}
	return routes
}

// Start starts the SNI router on the given address
func (r *Router) Start(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	r.mu.Lock()
	r.listener = listener
	r.mu.Unlock()

	log.Info().
		Str("addr", addr).
		Msg("[SNI] Router started")

	r.wg.Add(1)
	go r.acceptLoop(listener)

	return nil
}

// Stop stops the SNI router
func (r *Router) Stop() error {
	r.stopOnce.Do(func() {
		close(r.stopCh)

		r.mu.Lock()
		if r.listener != nil {
			r.listener.Close()
		}
		r.mu.Unlock()
	})

	r.wg.Wait()
	log.Info().Msg("[SNI] Router stopped")
	return nil
}

// Addr returns the router's listen address
func (r *Router) Addr() net.Addr {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.listener != nil {
		return r.listener.Addr()
	}
	return nil
}

// acceptLoop accepts incoming connections
func (r *Router) acceptLoop(listener net.Listener) {
	defer r.wg.Done()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-r.stopCh:
				return
			default:
				log.Error().Err(err).Msg("[SNI] Accept error")
				continue
			}
		}

		r.wg.Add(1)
		go r.handleConnection(conn)
	}
}

// handleConnection handles a single connection
func (r *Router) handleConnection(clientConn net.Conn) {
	defer r.wg.Done()

	// Set a deadline for reading the ClientHello
	clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))

	// Peek at the SNI from the ClientHello
	sni, peekedReader, err := PeekSNI(clientConn, maxTLSRecordSize)
	if err != nil {
		log.Error().
			Err(err).
			Str("remote", clientConn.RemoteAddr().String()).
			Msg("[SNI] Failed to extract SNI")
		clientConn.Close()
		return
	}

	// Clear the deadline
	clientConn.SetReadDeadline(time.Time{})

	// Find the route
	route, ok := r.GetRoute(sni)
	if !ok {
		log.Warn().
			Str("sni", sni).
			Str("remote", clientConn.RemoteAddr().String()).
			Msg("[SNI] No route found")
		clientConn.Close()
		return
	}

	log.Debug().
		Str("sni", sni).
		Str("target", route.TargetAddr).
		Str("remote", clientConn.RemoteAddr().String()).
		Msg("[SNI] Route found")

	// Wrap the connection so the callback can still read the peeked bytes.
	wrappedConn := &peekedConn{
		Conn:   clientConn,
		reader: peekedReader,
	}

	// Call the connection callback if set
	r.mu.RLock()
	onConnection := r.onConnection
	r.mu.RUnlock()

	if onConnection != nil {
		onConnection(wrappedConn, route)
		return
	}

	// Default behavior: proxy to target
	r.proxyConnection(clientConn, peekedReader, route)
}

// proxyConnection proxies data between client and target
func (r *Router) proxyConnection(clientConn net.Conn, clientReader io.Reader, route *Route) {
	defer clientConn.Close()

	// Connect to target
	targetConn, err := net.DialTimeout("tcp", route.TargetAddr, 10*time.Second)
	if err != nil {
		log.Error().
			Err(err).
			Str("target", route.TargetAddr).
			Str("sni", route.SNI).
			Msg("[SNI] Failed to connect to target")
		return
	}
	defer targetConn.Close()

	log.Info().
		Str("sni", route.SNI).
		Str("target", route.TargetAddr).
		Str("client", clientConn.RemoteAddr().String()).
		Msg("[SNI] Connection established")

	// Create error channels
	errCh := make(chan error, 2)

	// Client -> Target
	go func() {
		_, err := io.Copy(targetConn, clientReader)
		errCh <- err
		targetConn.Close()
	}()

	// Target -> Client
	go func() {
		_, err := io.Copy(clientConn, targetConn)
		errCh <- err
		clientConn.Close()
	}()

	// Wait for either direction to close
	<-errCh

	log.Debug().
		Str("sni", route.SNI).
		Str("target", route.TargetAddr).
		Msg("[SNI] Connection closed")
}

// BridgeConnections bridges two connections
func BridgeConnections(conn1, conn2 net.Conn) {
	defer conn1.Close()
	defer conn2.Close()

	errCh := make(chan error, 2)

	// Conn1 -> Conn2
	go func() {
		_, err := io.Copy(conn2, conn1)
		errCh <- err
		conn2.Close()
	}()

	// Conn2 -> Conn1
	go func() {
		_, err := io.Copy(conn1, conn2)
		errCh <- err
		conn1.Close()
	}()

	// Wait for either direction to close
	<-errCh
}

// ExtractSNIFromConnection extracts SNI from a connection without consuming data.
// It returns the SNI and a wrapped connection that includes the peeked data.
func ExtractSNIFromConnection(conn net.Conn, bufSize int) (string, net.Conn, error) {
	sni, reader, err := PeekSNI(conn, bufSize)
	if err != nil {
		return "", nil, err
	}

	// Wrap the connection to include the peeked data.
	wrappedConn := &peekedConn{
		Conn:   conn,
		reader: reader,
	}

	return sni, wrappedConn, nil
}

// peekedConn wraps a net.Conn to include peeked data
type peekedConn struct {
	net.Conn
	reader io.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}
