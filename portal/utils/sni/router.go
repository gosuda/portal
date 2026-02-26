package sni

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Route maps an SNI hostname to a lease.
type Route struct {
	LeaseID string
	SNI     string
}

// Router is a TCP listener that peeks the TLS ClientHello, extracts the
// SNI hostname, and routes the connection to the matching lease via a
// configurable callback.
type Router struct {
	addr     string
	listener net.Listener

	mu     sync.RWMutex
	routes map[string]*Route // SNI -> Route (exact + wildcard entries)
	leases map[string]*Route // leaseID -> Route (for cleanup by lease)

	onConnection func(conn net.Conn, route *Route)

	wg       sync.WaitGroup
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewRouter creates a new SNI router that will listen on addr (e.g. ":443").
func NewRouter(addr string) *Router {
	return &Router{
		addr:   addr,
		routes: make(map[string]*Route),
		leases: make(map[string]*Route),
		stopCh: make(chan struct{}),
	}
}

// SetConnectionCallback sets the function called when a connection matches
// a route. The callback receives the wrapped peekedConn (with ClientHello
// prepended) and the matched route.
func (r *Router) SetConnectionCallback(fn func(conn net.Conn, route *Route)) {
	r.mu.Lock()
	r.onConnection = fn
	r.mu.Unlock()
}

// RegisterRoute adds or updates an SNI route for a lease.
// If the lease previously had a different SNI, the old route is removed.
// If another lease occupied this SNI, that lease's route is removed.
func (r *Router) RegisterRoute(leaseID, sni string) {
	sni = strings.ToLower(sni)

	r.mu.Lock()
	defer r.mu.Unlock()

	// Clean up old route if lease re-registers with a different SNI.
	if oldRoute, ok := r.leases[leaseID]; ok && oldRoute.SNI != sni {
		delete(r.routes, oldRoute.SNI)
	}

	// If another lease occupied this SNI, remove that lease's mapping.
	if oldRoute, ok := r.routes[sni]; ok && oldRoute.LeaseID != leaseID {
		delete(r.leases, oldRoute.LeaseID)
	}

	route := &Route{LeaseID: leaseID, SNI: sni}
	r.routes[sni] = route
	r.leases[leaseID] = route

	log.Debug().Str("lease_id", leaseID).Str("sni", sni).Msg("[SNI] route registered")
}

// UnregisterRouteByLeaseID removes a route by lease ID.
func (r *Router) UnregisterRouteByLeaseID(leaseID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if route, ok := r.leases[leaseID]; ok {
		delete(r.routes, route.SNI)
		delete(r.leases, leaseID)
		log.Debug().Str("lease_id", leaseID).Msg("[SNI] route unregistered by lease")
	}
}

// ListenAndServe starts the TCP listener and handles incoming connections.
// Blocks until Stop is called or the listener errors.
func (r *Router) ListenAndServe() error {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", r.addr)
	if err != nil {
		return err
	}
	r.listener = ln

	log.Info().Str("addr", r.addr).Msg("[SNI] router listening")

	var conn net.Conn
	for {
		conn, err = ln.Accept()
		if err != nil {
			select {
			case <-r.stopCh:
				return nil
			default:
				log.Error().Err(err).Msg("[SNI] accept error")
				continue
			}
		}

		r.wg.Add(1)
		go r.handleConnection(conn)
	}
}

// Stop gracefully shuts down the router. Closes the listener and waits
// for all in-flight connections to drain.
func (r *Router) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
	if r.listener != nil {
		_ = r.listener.Close()
	}
	r.wg.Wait()
	log.Info().Msg("[SNI] router stopped")
}

// handleConnection peeks the SNI, matches a route, and calls the connection callback.
func (r *Router) handleConnection(conn net.Conn) {
	defer r.wg.Done()

	// Set read deadline for ClientHello (5s to prevent slowloris).
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	sniHost, peekedReader, sniErr := PeekSNI(conn)
	_ = conn.SetReadDeadline(time.Time{}) // clear deadline

	if sniErr != nil {
		log.Debug().Err(sniErr).Str("remote", conn.RemoteAddr().String()).Msg("[SNI] peek failed")
		_ = conn.Close()
		return
	}

	sniHost = strings.ToLower(sniHost)

	route := r.matchRoute(sniHost)
	if route == nil {
		log.Debug().Str("sni", sniHost).Msg("[SNI] no matching route")
		_ = conn.Close()
		return
	}

	// Wrap connection with peeked bytes.
	wrappedConn := NewPeekedConn(conn, peekedReader)

	r.mu.RLock()
	callback := r.onConnection
	r.mu.RUnlock()

	if callback == nil {
		log.Warn().Str("sni", sniHost).Msg("[SNI] no connection callback set")
		_ = conn.Close()
		return
	}

	callback(wrappedConn, route)
}

// matchRoute finds a route for the given SNI hostname.
// Tries exact match first, then progressively broader wildcards:
//
//	"foo.bar.example.com" -> exact -> "*.bar.example.com" -> "*.example.com" -> "*.com"
func (r *Router) matchRoute(sni string) *Route {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Exact match.
	if route, ok := r.routes[sni]; ok {
		return route
	}

	// Wildcard matching: strip leftmost label and try *.remainder.
	parts := strings.SplitN(sni, ".", 2)
	for len(parts) == 2 && parts[1] != "" {
		wildcard := "*." + parts[1]
		if route, ok := r.routes[wildcard]; ok {
			return route
		}
		parts = strings.SplitN(parts[1], ".", 2)
	}

	return nil
}
