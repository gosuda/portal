package sdk

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
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/relaydns/relaydns"
)

// ClientConfig configures the client. Defaults apply; set Handler or TargetTCP.
type ClientConfig struct {
	ServerURL   string
	Bootstraps  []string
	HTTPTimeout time.Duration
	PreferQUIC  bool
	PreferLocal bool

	// Protocol (e.g. "/relaydns/http/1.0"); Topic for adverts.
	Protocol string
	Topic    string

	AdvertiseEvery         time.Duration
	AdvertiseTTL           time.Duration
	RefreshBootstrapsEvery time.Duration

	Name string
	DNS  string

	// Set one: custom stream Handler, or TargetTCP to proxy bytes to a local TCP service.
	Handler   func(s network.Stream)
	TargetTCP string
}

// RelayClient advertises over pubsub and handles inbound streams.
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

// sensible defaults for optional client config
const (
	defaultAdvertiseEvery    = 5 * time.Second
	defaultHTTPTimeout       = 3 * time.Second
	defaultRefreshBootstraps = 20 * time.Second
)

// applyDefaults fills zero-values in cfg with sane defaults.
func applyDefaults(cfg ClientConfig) ClientConfig {
	if cfg.AdvertiseEvery <= 0 {
		cfg.AdvertiseEvery = defaultAdvertiseEvery
	}
	if cfg.AdvertiseTTL <= 0 {
		// Reduce default TTL from 50s to 15s (3x default advertise interval)
		cfg.AdvertiseTTL = 3 * cfg.AdvertiseEvery
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = defaultHTTPTimeout
	}
	if cfg.RefreshBootstrapsEvery <= 0 {
		cfg.RefreshBootstrapsEvery = defaultRefreshBootstraps
	}
	// Always prefer QUIC and local addresses for better performance and locality.
	cfg.PreferQUIC = true
	cfg.PreferLocal = true
	if cfg.Protocol == "" {
		cfg.Protocol = relaydns.DefaultProtocol
	}
	if cfg.Topic == "" {
		cfg.Topic = relaydns.DefaultTopic
	}
	return cfg
}

// NewClient creates a client (not started) with defaults applied.
func NewClient(ctx context.Context, cfg ClientConfig) (*RelayClient, error) {
	cfg = applyDefaults(cfg)

	h, err := relaydns.MakeHost(ctx, 0, true)
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

// Start wires handlers, joins pubsub, advertises, and refreshes bootstraps.
func (b *RelayClient) Start(ctx context.Context) error {
	if b.stop != nil {
		return fmt.Errorf("client already started")
	}
	// 1) resolve bootstraps (flags + optional server health)
	boot := b.resolveBootstraps()
	if len(boot) > 0 {
		relaydns.ConnectBootstraps(ctx, b.h, boot)
		// If a server URL is configured, only mark healthy if actually
		// connected to the server peer (not just able to fetch /hosts).
		if b.cfg.ServerURL != "" {
			if addrs, err := relaydns.FetchMultiaddrsFromHosts(b.cfg.ServerURL, b.cfg.HTTPTimeout); err == nil {
				if pid, ok := serverPeerFromAddrs(addrs); ok {
					if b.h.Network().Connectedness(pid) == network.Connected {
						b.setServerHealthy(true)
					} else {
						b.setServerHealthy(false)
					}
				}
			}
		}
	} else {
		log.Warn().Msg("relaydns: no bootstrap sources provided (Bootstraps/ServerURL); discovery may fail")
	}

	// 2) stream handler for inbound libp2p streams
	if err := b.setupStreamHandler(); err != nil {
		return err
	}

	// 3) pubsub join (for adverts)
	if err := b.joinPubSub(ctx); err != nil {
		return err
	}

	// 4) advertiser loop
	advCtx, cancel := context.WithCancel(ctx)
	b.stop = cancel
	b.startAdvertiser(advCtx)

	// 5) background: periodically re-fetch bootstraps from server and reconnect
	if b.cfg.ServerURL != "" {
		b.startRefreshLoop(advCtx)
	}

	if addrs := relaydns.BuildAddrs(b.Host()); len(addrs) > 0 {
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

// Close stops all loops, removes the handler, and closes the host.
func (b *RelayClient) Close() error {
	if b.stop != nil {
		b.stop()
	}
	b.wg.Wait()
	if b.t != nil {
		_ = b.t.Close()
	}
	if b.h != nil {
		if b.protoID != "" {
			b.h.RemoveStreamHandler(b.protoID)
		}
		return b.h.Close()
	}
	return nil
}

func (b *RelayClient) setServerHealthy(ok bool) {
	b.statusMu.Lock()
	b.serverHealthy = ok
	b.statusMu.Unlock()
}

// resolveBootstraps merges configured bootstraps with optional server-provided
// multiaddrs from /health. It updates serverHealthy accordingly.
func (b *RelayClient) resolveBootstraps() []string {
	boot := make([]string, 0, len(b.cfg.Bootstraps)+4)
	if len(b.cfg.Bootstraps) > 0 {
		boot = append(boot, b.cfg.Bootstraps...)
	}
	if b.cfg.ServerURL != "" {
		if addrs, err := relaydns.FetchMultiaddrsFromHosts(b.cfg.ServerURL, b.cfg.HTTPTimeout); err != nil {
			b.setServerHealthy(false)
			log.Warn().Err(err).Msgf("relaydns: fetch /hosts from %s failed", b.cfg.ServerURL)
		} else {
			// Do not mark healthy yet; only after verifying libp2p connectivity
			relaydns.SortMultiaddrs(addrs, b.cfg.PreferQUIC, b.cfg.PreferLocal)
			boot = append(boot, addrs...)
		}
	}
	return relaydns.RemoveDuplicate(boot)
}

// setupStreamHandler installs the handler. TargetTCP proxies stream <-> local TCP.
func (b *RelayClient) setupStreamHandler() error {
	switch {
	case b.cfg.Handler != nil:
		b.h.SetStreamHandler(b.protoID, b.cfg.Handler)
	case b.cfg.TargetTCP != "":
		b.h.SetStreamHandler(b.protoID, func(s network.Stream) {
			stream := s
			backendAddr := b.cfg.TargetTCP
			defer func() {
				if err := stream.Close(); err != nil {
					log.Debug().Err(err).Msg("relaydns: stream close")
				}
			}()

			backendConn, err := net.Dial("tcp", backendAddr)
			if err != nil {
				log.Error().Err(err).Msgf("relaydns: dial %s", backendAddr)
				return
			}
			defer func() {
				if err := backendConn.Close(); err != nil {
					log.Debug().Err(err).Msg("relaydns: conn close")
				}
			}()

			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); _, _ = io.Copy(backendConn, stream) }()
			go func() { defer wg.Done(); _, _ = io.Copy(stream, backendConn) }()
			wg.Wait()
		})
	default:
		return fmt.Errorf("relaydns: either Handler or TargetTCP must be set")
	}
	return nil
}

// joinPubSub creates a GossipSub instance and joins the advert topic.
func (b *RelayClient) joinPubSub(ctx context.Context) error {
	ps, err := pubsub.NewGossipSub(ctx, b.h, pubsub.WithMessageSigning(true))
	if err != nil {
		return err
	}
	t, err := ps.Join(b.cfg.Topic)
	if err != nil {
		return err
	}
	b.ps, b.t = ps, t
	return nil
}

// startAdvertiser periodically publishes Advertise messages with host addrs.
func (b *RelayClient) startAdvertiser(ctx context.Context) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ticker := time.NewTicker(b.cfg.AdvertiseEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				enc := relaydns.BuildAddrs(b.h)
				ad := relaydns.Advertise{
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
				_ = b.t.Publish(ctx, payload)
			}
		}
	}()
}

// startRefreshLoop periodically fetches /health and attempts (re)connects.
func (b *RelayClient) startRefreshLoop(ctx context.Context) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		t := time.NewTicker(b.cfg.RefreshBootstrapsEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				addrs, err := relaydns.FetchMultiaddrsFromHosts(b.cfg.ServerURL, b.cfg.HTTPTimeout)
				if err != nil {
					b.setServerHealthy(false)
					log.Warn().Err(err).Msgf("refresh /hosts from %s failed", b.cfg.ServerURL)
					continue
				}
				relaydns.SortMultiaddrs(addrs, b.cfg.PreferQUIC, b.cfg.PreferLocal)
				addrs = relaydns.RemoveDuplicate(addrs)
				if len(addrs) > 0 {
					// Always attempt (re)connect; ConnectBootstraps handles dedupe and quiet logging
					relaydns.ConnectBootstraps(ctx, b.h, addrs)
					// Only mark healthy if actually connected to server peer
					if pid, ok := serverPeerFromAddrs(addrs); ok {
						if b.h.Network().Connectedness(pid) == network.Connected {
							b.setServerHealthy(true)
						} else {
							b.setServerHealthy(false)
						}
					}
				}
			}
		}
	}()
}

// serverPeerFromAddrs extracts the first peer.ID found in the provided
// multiaddrs. The server advertises its own addrs including /p2p/<peer>.
func serverPeerFromAddrs(addrs []string) (peer.ID, bool) {
	for _, s := range addrs {
		m, err := ma.NewMultiaddr(s)
		if err != nil {
			continue
		}
		if ai, err := peer.AddrInfoFromP2pAddr(m); err == nil {
			return ai.ID, true
		}
	}
	return "", false
}

// ServerStatus returns "Connected", "Connecting...", or "N/A".
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
