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

// NewClient constructs a client with defaults applied and an initialized libp2p host.
// It does not start networking. Call Start(ctx) to begin handlers, pubsub, and advertising.
func NewClient(ctx context.Context, cfg ClientConfig) (*RelayClient, error) {
	if cfg.AdvertiseEvery <= 0 {
		cfg.AdvertiseEvery = 5 * time.Second
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 3 * time.Second
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
			log.Warn().Err(err).Msgf("relaydns: fetch /health from %s failed", b.cfg.ServerURL)
		} else {
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

	if addrs := b.Host().Addrs(); len(addrs) > 0 {
		for _, a := range addrs {
			log.Info().Msgf("[client] host addr: %s/p2p/%s", a.String(), b.Host().ID().String())
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
		// sanity check
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
