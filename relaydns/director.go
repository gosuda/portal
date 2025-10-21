package relaydns

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/rs/zerolog/log"
)

type Director struct {
	ctx       context.Context
	h         host.Host
	protocol  string
	topicName string
	sub       *pubsub.Subscription

	storeMu sync.Mutex
	store   map[string]HostEntry
	ttl     time.Duration
	deadTTL time.Duration
}

func NewDirector(ctx context.Context, h host.Host, protocol, topic string) (*Director, error) {
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return nil, err
	}
	t, err := ps.Join(topic)
	if err != nil {
		return nil, err
	}
	sub, err := t.Subscribe()
	if err != nil {
		return nil, err
	}
	d := &Director{
		ctx:       ctx,
		h:         h,
		protocol:  protocol,
		topicName: topic,
		sub:       sub,
		store:     map[string]HostEntry{},
		ttl:       15 * time.Second,
		deadTTL:   10 * time.Minute,
	}
	go d.collect()
	go d.gc()
	return d, nil
}

func (d *Director) Close() error {
	d.sub.Cancel()
	return nil
}

func (d *Director) collect() {
	for {
		msg, err := d.sub.Next(d.ctx)
		if err != nil {
			return
		}
		var ad Advertise
		if err := json.Unmarshal(msg.Data, &ad); err != nil {
			continue
		}
		var ai *peer.AddrInfo
		// pick any addr that includes /p2p/ (dnsaddr resolved entries will)
		for _, s := range ad.Addrs {
			m, err := ma.NewMultiaddr(s)
			if err != nil {
				continue
			}
			if a, err := peer.AddrInfoFromP2pAddr(m); err == nil {
				ai = a
				break
			}
		}
		if ai == nil {
			continue
		}
		now := time.Now()
		d.storeMu.Lock()
		_, existed := d.store[ad.Peer]
		d.store[ad.Peer] = HostEntry{Info: ad, AddrInfo: ai, LastSeen: now, Connected: true}
		// refresh picker snapshot
		snap := make([]HostEntry, 0, len(d.store))
		for _, v := range d.store {
			snap = append(snap, v)
		}
		d.storeMu.Unlock()
		if existed {
			log.Debug().Str("peer", ad.Peer).Str("name", ad.Name).Msg("director: updated client advert")
		} else {
			log.Info().Str("peer", ad.Peer).Str("name", ad.Name).Msg("director: added client")
		}
		_ = snap // snapshot kept local; selection handled explicitly via /peer
	}
}

func (d *Director) gc() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-t.C:
			now := time.Now()
			removed := make([]HostEntry, 0)
			d.storeMu.Lock()
			for k, v := range d.store {
				// Prefer client-provided TTL if present; fallback to default d.ttl
				ttl := d.ttl
				if v.Info.TTL > 0 {
					ttl = time.Duration(v.Info.TTL) * time.Second
				}
				// mark disconnected after ttl
				if now.Sub(v.LastSeen) > ttl && v.Connected {
					v.Connected = false
					d.store[k] = v
				}
				// remove only after extended dead TTL
				deadAfter := d.deadTTL
				if v.Info.TTL > 0 {
					da := time.Duration(v.Info.TTL) * time.Second * 5
					if da > deadAfter {
						deadAfter = da
					}
				}
				if now.Sub(v.LastSeen) > deadAfter {
					removed = append(removed, v)
					delete(d.store, k)
				}
			}
			snap := make([]HostEntry, 0, len(d.store))
			for _, v := range d.store {
				snap = append(snap, v)
			}
			d.storeMu.Unlock()
			for _, r := range removed {
				log.Info().Str("peer", r.Info.Peer).Str("name", r.Info.Name).Dur("idle", now.Sub(r.LastSeen)).Msg("director: removed stale client")
			}
			_ = snap
		}
	}
}

// Hosts returns a snapshot of current known hosts.
func (d *Director) Hosts() []HostEntry {
	d.storeMu.Lock()
	defer d.storeMu.Unlock()
	list := make([]HostEntry, 0, len(d.store))
	for _, v := range d.store {
		list = append(list, v)
	}
	// Sort by last-seen (most recent first)
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].LastSeen.After(list[j].LastSeen)
	})
	return list
}

// ProxyHTTP proxies the given HTTP request to the specified peer and writes the response to w.
func (d *Director) ProxyHTTP(w http.ResponseWriter, r *http.Request, peerID, pathSuffix string) {
	d.storeMu.Lock()
	entry, ok := d.store[peerID]
	d.storeMu.Unlock()
	if !ok || entry.AddrInfo == nil {
		http.Error(w, "peer not found", http.StatusNotFound)
		return
	}
	if err := d.h.Connect(d.ctx, *entry.AddrInfo); err != nil {
		log.Error().Err(err).Msgf("connect %s failed", entry.AddrInfo.ID)
		http.Error(w, "upstream connect failed", http.StatusBadGateway)
		return
	}
	s, err := d.h.NewStream(d.ctx, entry.AddrInfo.ID, protocolID(d.protocol))
	if err != nil {
		log.Error().Err(err).Msg("new stream")
		http.Error(w, "open stream failed", http.StatusBadGateway)
		return
	}
	defer s.Close()
	outReq := r.Clone(d.ctx)
	outReq.URL = &url.URL{Path: pathSuffix, RawQuery: r.URL.RawQuery}
	outReq.RequestURI = ""
	if err := outReq.Write(s); err != nil {
		log.Error().Err(err).Msg("write upstream request")
		http.Error(w, "write upstream failed", http.StatusBadGateway)
		return
	}
	br := bufio.NewReader(s)
	resp, err := http.ReadResponse(br, outReq)
	if err != nil {
		log.Error().Err(err).Msg("read upstream response")
		http.Error(w, "bad upstream response", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
