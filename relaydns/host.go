package relaydns

import (
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/rs/zerolog/log"
)

func MakeHost(ctx context.Context, port int, enableRelay bool) (host.Host, error) {
	addrs := []string{
		fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port),
		fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", port),
		fmt.Sprintf("/ip6/::/tcp/%d", port),
		fmt.Sprintf("/ip6/::/udp/%d/quic-v1", port),
	}

	opts := []libp2p.Option{
		libp2p.ListenAddrStrings(addrs...),
		libp2p.DefaultTransports, // TCP+QUIC
		libp2p.NATPortMap(),
		libp2p.EnableNATService(),   // AutoNAT helper
		libp2p.EnableHolePunching(), // DCUtR
		libp2p.DefaultSecurity,
		libp2p.DefaultMuxers,
	}
	if enableRelay {
		opts = append(opts, libp2p.EnableRelay()) // circuit relay (useful both as client & svc)
	}
	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, err
	}
	return h, nil
}

func ConnectBootstraps(ctx context.Context, h host.Host, addrs []string) {
	// Aggregate all multiaddrs per peer and dial them together so the dialer
	// can fall back from loopback to routable addresses.
	perPeer := make(map[peer.ID][]ma.Multiaddr)

	for _, s := range addrs {
		m, err := ma.NewMultiaddr(s)
		if err != nil {
			log.Warn().Err(err).Msgf("bootstrap bad multiaddr %q", s)
			continue
		}
		ai, err := peer.AddrInfoFromP2pAddr(m)
		if err != nil {
			log.Warn().Err(err).Msgf("bootstrap missing /p2p/ in %q", s)
			continue
		}
		// Prefer the addr(s) returned by AddrInfoFromP2pAddr (base part without /p2p)
		bases := ai.Addrs
		if len(bases) == 0 {
			// Fallback: try to decapsulate /p2p component manually
			dec := m
			// Construct a /p2p/<peerID> multiaddr for decapsulation
			p2pComp, derr := ma.NewMultiaddr(fmt.Sprintf("/p2p/%s", ai.ID.String()))
			if derr == nil {
				dec = m.Decapsulate(p2pComp)
			}
			if dec != nil && dec.String() != "" {
				bases = []ma.Multiaddr{dec}
			}
		}
		if len(bases) == 0 {
			continue
		}
		// Append while de-duplicating per peer, preserving original order.
		cur := perPeer[ai.ID]
		seen := make(map[string]struct{}, len(cur))
		for _, a := range cur {
			seen[a.String()] = struct{}{}
		}
		for _, a := range bases {
			if _, ok := seen[a.String()]; ok {
				continue
			}
			cur = append(cur, a)
			seen[a.String()] = struct{}{}
		}
		perPeer[ai.ID] = cur
	}

	// Now attempt a single connect per peer with all known addresses.
	for pid, maddrs := range perPeer {
		if h.Network().Connectedness(pid) == network.Connected {
			continue
		}
		info := peer.AddrInfo{ID: pid, Addrs: maddrs}
		if err := h.Connect(ctx, info); err != nil {
			log.Warn().Err(err).Msgf("bootstrap connect %s", pid)
		}
	}
}
