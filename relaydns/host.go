package relaydns

import (
	"context"
	"fmt"
	"log"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
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
	for _, s := range addrs {
		m, err := ma.NewMultiaddr(s)
		if err != nil {
			log.Printf("bootstrap bad multiaddr %q: %v", s, err)
			continue
		}
		ai, err := peer.AddrInfoFromP2pAddr(m)
		if err != nil {
			log.Printf("bootstrap missing /p2p/ in %q: %v", s, err)
			continue
		}
		if err := h.Connect(ctx, *ai); err != nil {
			log.Printf("bootstrap connect %s: %v", ai.ID, err)
		} else {
			log.Printf("connected bootstrap %s", ai.ID)
		}
	}
}
