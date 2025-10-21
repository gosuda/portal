package relaydns

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "net"
    "net/http"
    "net/url"
    "sort"
    "strings"
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
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 3 * time.Second
	}
	b := &RelayClient{
		h:       h,
		cfg:     cfg,
		protoID: protocol.ID(cfg.Protocol),
	}

	boot := make([]string, 0, len(cfg.Bootstraps)+4)
	if len(cfg.Bootstraps) > 0 {
		boot = append(boot, cfg.Bootstraps...)
	}
    if cfg.ServerURL != "" {
        if addrs, err := fetchMultiaddrsFromHealth(cfg.ServerURL, cfg.HTTPTimeout); err != nil {
            log.Warn().Err(err).Msgf("relaydns: fetch /health from %s failed", cfg.ServerURL)
        } else {
            sortMultiaddrs(addrs, cfg.PreferQUIC, cfg.PreferLocal)
            boot = append(boot, addrs...)
        }
    }
    boot = uniq(boot)
    if len(boot) > 0 {
        ConnectBootstraps(ctx, h, boot)
    } else {
        log.Warn().Msg("relaydns: no bootstrap sources provided (Bootstraps/ServerURL); discovery may fail")
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
                log.Error().Err(err).Msgf("relaydns: dial %s", cfg.TargetTCP)
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
func fetchMultiaddrsFromHealth(base string, timeout time.Duration) ([]string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("parse server-url: %w", err)
	}
	// ensure path ends with /health
	if !strings.HasSuffix(u.Path, "/health") {
		if u.Path == "" || u.Path == "/" {
			u.Path = "/health"
		} else {
			u.Path = strings.TrimSuffix(u.Path, "/") + "/health"
		}
	}
	client := &http.Client{Timeout: timeout}
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var payload struct {
		Status     string   `json:"status"`
		PeerID     string   `json:"peerId"`
		Multiaddrs []string `json:"multiaddrs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Status != "ok" {
		return nil, errors.New("health not ok")
	}
	addrs := make([]string, 0, len(payload.Multiaddrs))
	for _, s := range payload.Multiaddrs {
		// 아주 기본적인 sanity check
		if strings.Contains(s, "/p2p/") && (strings.Contains(s, "/ip4/") || strings.Contains(s, "/ip6/")) {
			addrs = append(addrs, s)
		}
	}
	return addrs, nil
}

func sortMultiaddrs(addrs []string, preferQUIC, preferLocal bool) {
	score := func(a string) int {
		sc := 0
		if preferQUIC && strings.Contains(a, "/quic-v1") {
			sc += 2
		}
		if preferLocal && (strings.Contains(a, "/ip4/127.0.0.1/") || strings.Contains(a, "/ip6/::1/")) {
			sc += 1
		}
		return sc
	}
	sort.SliceStable(addrs, func(i, j int) bool { return score(addrs[i]) > score(addrs[j]) })
}

func uniq(ss []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
