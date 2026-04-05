// Package ols provides the OLS routing engine, isolating all paired Reverse
// Siamese routing logic and the inter-relay wire protocol from the portal server core.
package ols

import (
	"context"
	"io"
	"net"
	"time"

	"github.com/gosuda/keyless_tls/relay/l4"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/portal/transport"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

const (
	interRelayPort       = 7778
	clientHelloWait      = 2 * time.Second
	maxRouteContextBytes = transport.OnionCellSize
	defaultMaxHops       = 3
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

// Engine encapsulates OLS routing: Reverse Siamese grid management, inter-relay protocol,
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
		HopCount:     0,
		MaxHops:      defaultMaxHops,
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
	meta := transport.NewMeta(routeCtx.MaxHops)
	layer := transport.OnionLayer{
		Meta:        meta,
		NextHopHint: transport.HashNodeID(targetID),
	}
	cell, err := transport.EncodeOnionLayer(layer, transport.NewNoopCipher())
	if err != nil {
		_ = targetConn.Close()
		return false
	}
	if err = writeOnionCell(targetConn, cell); err != nil {
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

	cell, err := readOnionCell(conn)
	if err != nil {
		log.Warn().Err(err).Msg("ols: failed to read onion cell")
		return
	}
	layer, err := transport.DecodeOnionLayer(cell, transport.NewNoopCipher())
	if err != nil {
		log.Warn().Err(err).Msg("ols: failed to decode onion cell")
		return
	}
	if !transport.HintMatches(e.selfKey, layer.NextHopHint) {
		log.Warn().Msg("ols: onion hint mismatch; dropping connection")
		return
	}
	meta := layer.Meta
	meta = meta.Advance()
	routeCtx := &policy.RouteContext{
		OriginNodeID: e.selfKey,
		HopCount:     int(meta.Hop),
		MaxHops:      int(meta.MaxHops),
	}

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
					nextLayer := transport.OnionLayer{
						Meta:        meta,
						NextHopHint: transport.HashNodeID(targetID),
					}
					nextCell, encErr := transport.EncodeOnionLayer(nextLayer, transport.NewNoopCipher())
					if encErr == nil {
						if writeErr := writeOnionCell(targetConn, nextCell); writeErr == nil {
							bridge(wrappedConn, targetConn)
							e.manager.MarkSuccess(targetID)
							return
						}
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

func writeOnionCell(conn net.Conn, cell transport.Cell) error {
	_, err := conn.Write(cell.Buffer[:])
	return err
}

func readOnionCell(conn net.Conn) (transport.Cell, error) {
	var cell transport.Cell
	if _, err := io.ReadFull(conn, cell.Buffer[:]); err != nil {
		return cell, err
	}
	return cell, nil
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
