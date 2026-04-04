// Package ols provides the OLS routing engine, isolating all Orthogonal Latin
// Square routing logic and the inter-relay wire protocol from the portal server core.
package ols

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"

	"github.com/gosuda/keyless_tls/relay/l4"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

const (
	interRelayPort       = 7778
	routeContextMagic    = "PORT"
	clientHelloWait      = 2 * time.Second
	maxRouteContextBytes = 65536
)

// Dialer dials an outbound TCP connection. It mirrors net.Dialer's context-aware
// dialing method so the engine avoids depending on specific transport types.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// PeerResolver maps a node identity key to the TCP dial address of its
// inter-relay endpoint. Implementations hide all transport-specific details
// (overlay IPs, peer key checks, etc.) from the engine.
type PeerResolver interface {
	// PeerAddr returns the host:port to dial for nodeID and true if the node
	// is reachable as an overlay peer.  Returns "", false when the node
	// cannot be reached (not configured, marked down, etc.).
	PeerAddr(nodeID string) (addr string, ok bool)
}

// PeerDialer combines address resolution and connection dialing into a single
// interface that the engine uses for inter-relay forwarding.  Callers that
// implement both PeerResolver and Dialer satisfy PeerDialer.
type PeerDialer interface {
	PeerResolver
	Dialer
}

// Engine encapsulates OLS routing: MOLS grid management, inter-relay protocol,
// and connection forwarding decisions. It intentionally avoids server
// lifecycle concerns (listeners, identity key derivation) and transport-specific
// protocol details (overlay addresses, peer keys, etc.).
type Engine struct {
	selfKey string
	manager *policy.OLSManager
}

// New returns a new Engine for the given self node identity key.
func New(selfKey string) *Engine {
	e := &Engine{
		selfKey: selfKey,
		manager: policy.NewOLSManager(),
	}
	e.manager.UpdateNodes([]string{selfKey})
	return e
}

// OnRelaySetChanged updates the OLS grid topology and node load scores from the
// latest relay-set snapshot.  Call this whenever the relay set changes.
func (e *Engine) OnRelaySetChanged(localLoad policy.NodeLoad, snapshot map[string]types.RelayState) {
	nodes := []string{e.selfKey}
	for id, state := range snapshot {
		if !state.Expired {
			nodes = append(nodes, id)
		}
	}
	e.manager.UpdateNodes(nodes)
	e.manager.UpdateLoad(e.selfKey, localLoad, 0, time.Now().Unix())
	for id, state := range snapshot {
		if !state.Expired {
			e.manager.UpdateLoad(id, policy.NodeLoad{}, state.Descriptor.LoadScore, state.Descriptor.LastUpdated)
		}
	}
}

// RouteConn attempts to route conn to the best OLS target node.
// peers resolves node identity keys to dial addresses and dials the connection;
// it encapsulates all transport-specific knowledge so the engine remains
// protocol-agnostic.
// Returns true if conn was handled (proxied away); false means the caller
// should serve it locally.
func (e *Engine) RouteConn(ctx context.Context, conn net.Conn, serverName string, peers PeerDialer) bool {
	if peers == nil {
		return false
	}
	routeCtx := &policy.RouteContext{
		OriginNodeID: e.selfKey,
		Visited:      []string{e.selfKey},
	}
	targetID, err := e.manager.GetTargetNodeID(conn.RemoteAddr().String(), serverName, routeCtx)
	if err != nil || targetID == e.selfKey {
		return false
	}
	proxyAddr, ok := peers.PeerAddr(targetID)
	if !ok {
		return false
	}
	targetConn, err := peers.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		e.manager.MarkFailure(targetID)
		log.Warn().Err(err).Str("target", proxyAddr).Msg("ols: failed to dial inter-relay target")
		return false
	}
	if err = writeRouteContext(targetConn, routeCtx); err != nil {
		_ = targetConn.Close()
		e.manager.MarkFailure(targetID)
		return false
	}
	log.Debug().
		Str("server_name", serverName).
		Str("target_id", targetID).
		Msg("ols: proxying to target node")
	bridge(conn, targetConn)
	e.manager.MarkSuccess(targetID)
	return true
}

// ServeInterRelayConn handles one connection received on the inter-relay port.
// peers resolves node addresses and dials; serveLocal is called with the
// unwrapped connection when the hop should be served locally.
func (e *Engine) ServeInterRelayConn(
	ctx context.Context,
	conn net.Conn,
	peers PeerDialer,
	serveLocal func(serverName string, conn net.Conn),
) {
	defer conn.Close()

	routeCtx, err := readRouteContext(conn)
	if err != nil {
		log.Warn().Err(err).Msg("ols: failed to read inter-relay route context")
		return
	}
	routeCtx.Visited = append(routeCtx.Visited, e.selfKey)
	routeCtx.HopCount++

	clientHello, wrappedConn, err := l4.InspectClientHello(conn, clientHelloWait)
	if err != nil {
		return
	}
	serverName := utils.NormalizeHostname(clientHello.ServerName)
	if serverName == "" {
		_ = wrappedConn.Close()
		return
	}

	// Attempt to re-route via OLS when peers are available.
	if peers != nil {
		targetID, routeErr := e.manager.GetTargetNodeID(conn.RemoteAddr().String(), serverName, routeCtx)
		if routeErr == nil && targetID != e.selfKey {
			if proxyAddr, ok := peers.PeerAddr(targetID); ok {
				targetConn, dialErr := peers.DialContext(ctx, "tcp", proxyAddr)
				if dialErr == nil {
					if writeErr := writeRouteContext(targetConn, routeCtx); writeErr == nil {
						bridge(wrappedConn, targetConn)
						e.manager.MarkSuccess(targetID)
						return
					}
					_ = targetConn.Close()
				}
				e.manager.MarkFailure(targetID)
			}
		}
	}

	// Fall through to local handling.
	serveLocal(serverName, wrappedConn)
}

// --- inter-relay wire protocol ---

func writeRouteContext(conn net.Conn, ctx *policy.RouteContext) error {
	data, err := json.Marshal(ctx)
	if err != nil {
		return err
	}
	header := make([]byte, 8)
	copy(header[:4], routeContextMagic)
	binary.BigEndian.PutUint32(header[4:], uint32(len(data)))
	if _, err = conn.Write(header); err != nil {
		return err
	}
	_, err = conn.Write(data)
	return err
}

func readRouteContext(conn net.Conn) (*policy.RouteContext, error) {
	header := make([]byte, 8)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	if string(header[:4]) != routeContextMagic {
		return nil, errors.New("ols: invalid inter-relay magic")
	}
	length := binary.BigEndian.Uint32(header[4:])
	if length > maxRouteContextBytes {
		return nil, errors.New("ols: inter-relay context too large")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(conn, data); err != nil {
		return nil, err
	}
	var ctx policy.RouteContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, err
	}
	return &ctx, nil
}

// bridge copies bidirectionally between left and right, then closes both.
func bridge(left, right net.Conn) {
	defer left.Close()
	defer right.Close()
	done := make(chan struct{}, 2)
	copyHalf := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		closeWrite(dst)
		done <- struct{}{}
	}
	go copyHalf(right, left)
	go copyHalf(left, right)
	<-done
	<-done
}

func closeWrite(conn net.Conn) {
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := conn.(closeWriter); ok {
		_ = cw.CloseWrite()
	}
}
