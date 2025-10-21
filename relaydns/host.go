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
	// Connect once per unique peer ID; skip already-connected peers to avoid noisy logs.
	seen := make(map[string]struct{}, len(addrs))
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
		pid := ai.ID.String()
		if _, ok := seen[pid]; ok {
			continue
		}
		seen[pid] = struct{}{}

		if h.Network().Connectedness(ai.ID) == network.Connected {
			// Already connected: skip loudly logging.
			continue
		}
		if err := h.Connect(ctx, *ai); err != nil {
			// Only warn on errors
			log.Warn().Err(err).Msgf("bootstrap connect %s", ai.ID)
		}
	}
}
