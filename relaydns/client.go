package relaydns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

type ClientConfig struct {
	// libp2p stream protocol id (e.g. "/relaydns/ssh/1.0")
	Protocol string
	// pubsub topic for backend adverts (e.g. "relaydns.backends")
	Topic string
	// advertise interval
	AdvertiseEvery time.Duration
	// optional metadata
	Name string
	DNS  string

	// One of the following:
	// 1) Provide a custom stream handler
	Handler func(s network.Stream)
	// 2) Or just set TargetTCP to auto-pipe bytes to a local TCP service (e.g. "127.0.0.1:22")
	TargetTCP string
}

type RelayClient struct {
	h       host.Host
	cfg     ClientConfig
	protoID protocol.ID

	ps   *pubsub.PubSub
	t    *pubsub.Topic
	wg   sync.WaitGroup
	stop context.CancelFunc
}

// NewClient wires a reusable backend node that other apps can embed.
// It registers a stream handler and starts an advertiser loop.
// Call Close() to stop.
func NewClient(ctx context.Context, h host.Host, cfg ClientConfig) (*RelayClient, error) {
	if cfg.Protocol == "" {
		cfg.Protocol = "/relaydns/ssh/1.0"
	}
	if cfg.Topic == "" {
		cfg.Topic = "relaydns.backends"
	}
	if cfg.AdvertiseEvery <= 0 {
		cfg.AdvertiseEvery = 5 * time.Second
	}
	b := &RelayClient{
		h:       h,
		cfg:     cfg,
		protoID: protocol.ID(cfg.Protocol),
	}

	// 1) stream handler
	switch {
	case cfg.Handler != nil:
		h.SetStreamHandler(b.protoID, cfg.Handler)
	case cfg.TargetTCP != "":
		h.SetStreamHandler(b.protoID, func(s network.Stream) {
			defer s.Close()
			c, err := net.Dial("tcp", cfg.TargetTCP)
			if err != nil {
				log.Printf("relaydns: dial %s: %v", cfg.TargetTCP, err)
				return
			}
			defer c.Close()
			// raw byte pipe
			go io.Copy(c, s)
			io.Copy(s, c)
		})
	default:
		return nil, fmt.Errorf("relaydns: either Handler or TargetTCP must be set")
	}

	// 2) pubsub join
	ps, err := pubsub.NewGossipSub(ctx, h, pubsub.WithMessageSigning(true))
	if err != nil {
		return nil, err
	}
	t, err := ps.Join(cfg.Topic)
	if err != nil {
		return nil, err
	}
	b.ps, b.t = ps, t

	// 3) advertiser loop
	advCtx, cancel := context.WithCancel(ctx)
	b.stop = cancel
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ticker := time.NewTicker(cfg.AdvertiseEvery)
		defer ticker.Stop()
		for {
			select {
			case <-advCtx.Done():
				return
			case <-ticker.C:
				addrs := b.h.Addrs()
				enc := make([]string, 0, len(addrs))
				for _, a := range addrs {
					enc = append(enc, fmt.Sprintf("%s/p2p/%s", a.String(), b.h.ID().String()))
				}
				ad := Advertise{
					Peer:  b.h.ID().String(),
					Name:  b.cfg.Name,
					DNS:   b.cfg.DNS,
					Addrs: enc,
					Ready: true,
					Load:  0.0,
					TS:    time.Now().UTC(),
				}
				payload, _ := json.Marshal(ad)
				_ = b.t.Publish(advCtx, payload)
			}
		}
	}()

	return b, nil
}

func (b *RelayClient) Close() error {
	if b.stop != nil {
		b.stop()
	}
	b.wg.Wait()
	// leaving topic is optional; libp2p will clean up on host close
	return nil
}
