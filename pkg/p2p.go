package pkg

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
)

type Advertise struct {
	Peer  string    `json:"peer"`
	Name  string    `json:"name,omitempty"`
	DNS   string    `json:"dns,omitempty"`
	Addrs []string  `json:"addrs"`
	Ready bool      `json:"ready"`
	Load  float64   `json:"load"`
	TS    time.Time `json:"ts"`
}

type HostEntry struct {
	Info     Advertise
	AddrInfo *peer.AddrInfo
	LastSeen time.Time
}

type Picker struct {
	mu     sync.RWMutex
	rr     uint64
	list   []HostEntry
	pinTo  string
	pinTil time.Time
}

func (p *Picker) update(list []HostEntry) {
	p.mu.Lock()
	p.list = list
	p.mu.Unlock()
}
func (p *Picker) choose() (HostEntry, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.list) == 0 {
		return HostEntry{}, false
	}
	if p.pinTo != "" && time.Now().Before(p.pinTil) {
		for _, e := range p.list {
			if e.Info.Peer == p.pinTo {
				return e, true
			}
		}
	}
	i := atomic.AddUint64(&p.rr, 1)
	return p.list[i%uint64(len(p.list))], true
}
func (p *Picker) pin(peerID string, dur time.Duration) {
	p.mu.Lock()
	p.pinTo = peerID
	p.pinTil = time.Now().Add(dur)
	p.mu.Unlock()
}
func (p *Picker) unpin() {
	p.mu.Lock()
	p.pinTo = ""
	p.pinTil = time.Time{}
	p.mu.Unlock()
}

// ---- libp2p host boot ----

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

// ---- director ----

type Director struct {
	ctx       context.Context
	h         host.Host
	protocol  string
	topicName string
	sub       *pubsub.Subscription

	storeMu sync.Mutex
	store   map[string]HostEntry
	ttl     time.Duration
	pick    *Picker
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
		ttl:       45 * time.Second,
		pick:      &Picker{},
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
		d.storeMu.Lock()
		d.store[ad.Peer] = HostEntry{Info: ad, AddrInfo: ai, LastSeen: time.Now()}
		// refresh picker snapshot
		snap := make([]HostEntry, 0, len(d.store))
		for _, v := range d.store {
			snap = append(snap, v)
		}
		d.storeMu.Unlock()
		d.pick.update(snap)
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
			d.storeMu.Lock()
			for k, v := range d.store {
				if now.Sub(v.LastSeen) > d.ttl {
					delete(d.store, k)
				}
			}
			snap := make([]HostEntry, 0, len(d.store))
			for _, v := range d.store {
				snap = append(snap, v)
			}
			d.storeMu.Unlock()
			d.pick.update(snap)
		}
	}
}

func (d *Director) ServeTCP(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("director TCP listening on %s (protocol %s)", addr, d.protocol)
	for {
		c, err := ln.Accept()
		if err != nil {
			continue
		}
		go d.handleConn(c)
	}
}

func (d *Director) handleConn(c net.Conn) {
	defer c.Close()
	entry, ok := d.pick.choose()
	if !ok {
		log.Println("no backend peers available")
		return
	}
	// ensure we're connected (AddrInfo contains addrs)
	if err := d.h.Connect(d.ctx, *entry.AddrInfo); err != nil {
		log.Printf("connect %s failed: %v", entry.AddrInfo.ID, err)
		return
	}
	s, err := d.h.NewStream(d.ctx, entry.AddrInfo.ID, protocolID(d.protocol))
	if err != nil {
		log.Printf("new stream: %v", err)
		return
	}
	defer s.Close()
	// raw byte tunnel
	go io.Copy(s, c)
	io.Copy(c, s)
}

func (d *Director) ServeHTTP(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/hosts", func(w http.ResponseWriter, r *http.Request) {
		d.storeMu.Lock()
		defer d.storeMu.Unlock()
		list := make([]HostEntry, 0, len(d.store))
		for _, v := range d.store {
			list = append(list, v)
		}
		_ = json.NewEncoder(w).Encode(list)
	})
	mux.HandleFunc("/override", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			peerID := r.URL.Query().Get("peer")
			dur := 30 * time.Second
			if s := r.URL.Query().Get("ttl"); s != "" {
				if v, err := time.ParseDuration(s); err == nil {
					dur = v
				}
			}
			d.pick.pin(peerID, dur)
			w.WriteHeader(204)
		case "DELETE":
			d.pick.unpin()
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	log.Printf("director HTTP API on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func protocolID(s string) protocol.ID {
	return protocol.ID(s)
}
