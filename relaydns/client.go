package relaydns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/rs/zerolog/log"
)

type ClientConfig struct {
	ServerURL   string
	Bootstraps  []string
	HTTPTimeout time.Duration
	PreferQUIC  bool
	PreferLocal bool

	// libp2p stream protocol id (e.g. "/relaydns/ssh/1.0")
	Protocol string
	// pubsub topic for backend adverts (e.g. "relaydns.backends")
	Topic string
	// advertise interval
	AdvertiseEvery time.Duration
	// advertise TTL (how long server should keep this entry alive)
	AdvertiseTTL time.Duration
	// how often to refresh server health/bootstraps (if ServerURL set)
	RefreshBootstrapsEvery time.Duration
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

	statusMu      sync.RWMutex
	serverHealthy bool
}

// NewClient constructs a client with defaults applied and an initialized libp2p host.
// It does not start networking. Call Start(ctx) to begin handlers, pubsub, and advertising.
func NewClient(ctx context.Context, cfg ClientConfig) (*RelayClient, error) {
	if cfg.AdvertiseEvery <= 0 {
		cfg.AdvertiseEvery = 5 * time.Second
	}
	if cfg.AdvertiseTTL <= 0 {
		cfg.AdvertiseTTL = 10 * cfg.AdvertiseEvery
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 3 * time.Second
	}
	if cfg.RefreshBootstrapsEvery <= 0 {
		cfg.RefreshBootstrapsEvery = 20 * time.Second
	}
	if cfg.Protocol == "" {
		cfg.Protocol = "/relaydns/http/1.0"
	}
	if cfg.Topic == "" {
		cfg.Topic = "relaydns.backends"
	}

	h, err := MakeHost(ctx, 0, true)
	if err != nil {
		return nil, fmt.Errorf("make host: %w", err)
	}
	b := &RelayClient{
		h:       h,
		cfg:     cfg,
		protoID: protocol.ID(cfg.Protocol),
	}
	return b, nil
}

// Start connects bootstraps, sets stream handler, joins pubsub, and starts advertising.
func (b *RelayClient) Start(ctx context.Context) error {
	if b.stop != nil {
		return fmt.Errorf("client already started")
	}
	// resolve bootstraps (from flags and optional server health)
	boot := make([]string, 0, len(b.cfg.Bootstraps)+4)
	if len(b.cfg.Bootstraps) > 0 {
		boot = append(boot, b.cfg.Bootstraps...)
	}
	if b.cfg.ServerURL != "" {
		if addrs, err := fetchMultiaddrsFromHealth(b.cfg.ServerURL, b.cfg.HTTPTimeout); err != nil {
			b.setServerHealthy(false)
			log.Warn().Err(err).Msgf("relaydns: fetch /health from %s failed", b.cfg.ServerURL)
		} else {
			b.setServerHealthy(true)
			sortMultiaddrs(addrs, b.cfg.PreferQUIC, b.cfg.PreferLocal)
			boot = append(boot, addrs...)
		}
	}
	boot = uniq(boot)
	if len(boot) > 0 {
		ConnectBootstraps(ctx, b.h, boot)
	} else {
		log.Warn().Msg("relaydns: no bootstrap sources provided (Bootstraps/ServerURL); discovery may fail")
	}

	// stream handler
	switch {
	case b.cfg.Handler != nil:
		b.h.SetStreamHandler(b.protoID, b.cfg.Handler)
	case b.cfg.TargetTCP != "":
		b.h.SetStreamHandler(b.protoID, func(s network.Stream) {
			defer s.Close()
			c, err := net.Dial("tcp", b.cfg.TargetTCP)
			if err != nil {
				log.Error().Err(err).Msgf("relaydns: dial %s", b.cfg.TargetTCP)
				return
			}
			defer c.Close()
			// raw byte pipe
			go io.Copy(c, s)
			io.Copy(s, c)
		})
	default:
		return fmt.Errorf("relaydns: either Handler or TargetTCP must be set")
	}

	// pubsub join
	ps, err := pubsub.NewGossipSub(ctx, b.h, pubsub.WithMessageSigning(true))
	if err != nil {
		return err
	}
	t, err := ps.Join(b.cfg.Topic)
	if err != nil {
		return err
	}
	b.ps, b.t = ps, t

	// advertiser loop
	advCtx, cancel := context.WithCancel(ctx)
	b.stop = cancel
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ticker := time.NewTicker(b.cfg.AdvertiseEvery)
		defer ticker.Stop()
		for {
			select {
			case <-advCtx.Done():
				return
			case <-ticker.C:
				enc := BuildAddrs(b.h)
				ad := Advertise{
					Peer:  b.h.ID().String(),
					Name:  b.cfg.Name,
					DNS:   b.cfg.DNS,
					Addrs: enc,
					Ready: true,
					Load:  0.0,
					TS:    time.Now().UTC(),
					TTL:   int(b.cfg.AdvertiseTTL.Seconds()),
					Proto: string(b.protoID),
				}
				payload, _ := json.Marshal(ad)
				_ = b.t.Publish(advCtx, payload)
			}
		}
	}()

	// background: periodically re-fetch bootstraps from server and reconnect
	if b.cfg.ServerURL != "" {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			t := time.NewTicker(b.cfg.RefreshBootstrapsEvery)
			defer t.Stop()
			for {
				select {
				case <-advCtx.Done():
					return
				case <-t.C:
					addrs, err := fetchMultiaddrsFromHealth(b.cfg.ServerURL, b.cfg.HTTPTimeout)
					if err != nil {
						b.setServerHealthy(false)
						log.Warn().Err(err).Msgf("refresh /health from %s failed", b.cfg.ServerURL)
						continue
					}
					b.setServerHealthy(true)
					sortMultiaddrs(addrs, b.cfg.PreferQUIC, b.cfg.PreferLocal)
					addrs = uniq(addrs)
					if len(addrs) > 0 {
						// Always attempt (re)connect; ConnectBootstraps handles dedupe and quiet logging
						ConnectBootstraps(ctx, b.h, addrs)
					}
				}
			}
		}()
	}

	if addrs := BuildAddrs(b.Host()); len(addrs) > 0 {
		for _, s := range addrs {
			log.Info().Msgf("[client] host addr: %s", s)
		}
	} else {
		log.Info().Msgf("[client] host peer: %s (no listen addrs yet)", b.Host().ID().String())
	}

	return nil
}

func (b *RelayClient) Host() host.Host {
	return b.h
}

func (b *RelayClient) Close() error {
	if b.stop != nil {
		b.stop()
	}
	b.wg.Wait()
	// leaving topic is optional; libp2p will clean up on host close
	return nil
}

func (b *RelayClient) setServerHealthy(ok bool) {
	b.statusMu.Lock()
	b.serverHealthy = ok
	b.statusMu.Unlock()
}

// ServerStatus returns a human-friendly status regarding connection to ServerURL.
// If no ServerURL is configured, returns "N/A".
func (b *RelayClient) ServerStatus() string {
	if b.cfg.ServerURL == "" {
		return "N/A"
	}
	b.statusMu.RLock()
	ok := b.serverHealthy
	b.statusMu.RUnlock()
	if ok {
		return "Connected"
	}
	return "Connecting..."
}
