package relaydns

import (
	"context"
	"log"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

func MakeHost(ctx context.Context, enableRelay bool) (host.Host, error) {
	opts := []libp2p.Option{
		libp2p.DefaultTransports,    // TCP+QUIC
		libp2p.EnableNATService(),   // AutoNAT helper
		libp2p.EnableHolePunching(), // DCUtR
		libp2p.DefaultSecurity,
		libp2p.DefaultMuxers,
		libp2p.EnableAutoRelay(), // ← 추가
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
