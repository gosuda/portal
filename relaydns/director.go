package relaydns

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
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
		now := time.Now()
		d.storeMu.Lock()
		_, existed := d.store[ad.Peer]
		d.store[ad.Peer] = HostEntry{Info: ad, AddrInfo: ai, LastSeen: now}
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
			removed := make([]HostEntry, 0)
			d.storeMu.Lock()
			for k, v := range d.store {
				if now.Sub(v.LastSeen) > d.ttl {
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
			d.pick.update(snap)
		}
	}
}

func (d *Director) ServeTCP(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	log.Info().Msgf("director TCP listening on %s (protocol %s)", addr, d.protocol)
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
		log.Warn().Msg("no backend peers available")
		return
	}
	// ensure we're connected (AddrInfo contains addrs)
	if err := d.h.Connect(d.ctx, *entry.AddrInfo); err != nil {
		log.Error().Err(err).Msgf("connect %s failed", entry.AddrInfo.ID)
		return
	}
	s, err := d.h.NewStream(d.ctx, entry.AddrInfo.ID, protocolID(d.protocol))
	if err != nil {
		log.Error().Err(err).Msg("new stream")
		return
	}
	defer s.Close()
	// raw byte tunnel
	go io.Copy(s, c)
	io.Copy(c, s)
}

func (d *Director) ServeHTTP(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", d.handleIndex)
	mux.HandleFunc("/peer/", d.handlePeerProxy)
	mux.HandleFunc("/hosts", d.handleHosts)
	mux.HandleFunc("/override", d.handleOverride)
	mux.HandleFunc("/health", d.handleHealth)
	log.Info().Msgf("director HTTP API on %s", addr)
	return http.ListenAndServe(addr, mux)
}

var adminIndexTmpl = template.Must(template.New("admin-index").Parse(`<!doctype html>
<html>
<head>
  <meta charset="utf-8"/>
  <title>RelayDNS Admin</title>
  <style>
    body { font-family: system-ui, sans-serif; margin: 24px; }
    h1 { margin: 0 0 16px 0; }
    .card { border: 1px solid #ddd; border-radius: 10px; padding: 16px; margin: 12px 0; }
    .row { display: flex; gap: 16px; align-items: center; flex-wrap: wrap; }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
    input[type=text] { padding: 6px 8px; border: 1px solid #ccc; border-radius: 6px; min-width: 260px; }
    a.btn { text-decoration: none; background:#2d6cdf; color:white; padding:6px 10px; border-radius:6px; }
    small { color:#666 }
  </style>
  <script>
    function goToPeer(peerId){
      const inp = document.getElementById('path-'+peerId);
      const path = inp && inp.value ? ('/' + inp.value.replace(/^\/+/, '')) : '/';
      window.location.href = '/peer/' + peerId + path;
      return false;
    }
  </script>
  </head>
<body>
  <h1>RelayDNS Admin</h1>
  <p>Known clients: {{len .Rows}}</p>
  {{range .Rows}}
    <div class="card">
      <div class="row">
        <b>{{if .Name}}{{.Name}}{{else}}(unnamed){{end}}</b>
        <span class="mono">{{.Peer}}</span>
        {{if .DNS}}<span>DNS: <span class="mono">{{.DNS}}</span></span>{{end}}
        <small>last seen: {{.LastSeen}}</small>
      </div>
      <div class="row" style="margin-top:8px;">
        <a class="btn" href="{{.Link}}">Open</a>
        <form onsubmit="return goToPeer('{{.Peer}}')">
          <input id="path-{{.Peer}}" type="text" placeholder="optional path, e.g. api/health" />
          <button class="btn" type="submit">Open Path</button>
        </form>
      </div>
    </div>
  {{else}}
    <p>No clients discovered yet. Ensure backends are advertising and bootstraps are configured.</p>
  {{end}}
</body>
</html>`))

func (d *Director) handleIndex(w http.ResponseWriter, r *http.Request) {
	type row struct {
		Peer     string
		Name     string
		DNS      string
		LastSeen string
		Link     string
	}
	type page struct{ Rows []row }
	d.storeMu.Lock()
	rows := make([]row, 0, len(d.store))
	for _, v := range d.store {
		rows = append(rows, row{
			Peer:     v.Info.Peer,
			Name:     v.Info.Name,
			DNS:      v.Info.DNS,
			LastSeen: time.Since(v.LastSeen).Round(time.Second).String() + " ago",
			Link:     "/peer/" + v.Info.Peer + "/",
		})
	}
	d.storeMu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = adminIndexTmpl.Execute(w, page{Rows: rows})
}

func (d *Director) handlePeerProxy(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/peer/")
	parts := strings.SplitN(p, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "missing peer id", http.StatusBadRequest)
		return
	}
	peerID := parts[0]
	pathSuffix := "/"
	if len(parts) == 2 {
		pathSuffix = "/" + parts[1]
	}

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

func (d *Director) handleHosts(w http.ResponseWriter, r *http.Request) {
	d.storeMu.Lock()
	defer d.storeMu.Unlock()
	list := make([]HostEntry, 0, len(d.store))
	for _, v := range d.store {
		list = append(list, v)
	}
	_ = json.NewEncoder(w).Encode(list)
}

func (d *Director) handleOverride(w http.ResponseWriter, r *http.Request) {
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
		log.Info().Str("peer", peerID).Dur("ttl", dur).Msg("director: pin override")
		w.WriteHeader(204)
	case "DELETE":
		d.pick.unpin()
		log.Info().Msg("director: unpin override")
		w.WriteHeader(204)
	default:
		w.WriteHeader(405)
	}
}

func (d *Director) handleHealth(w http.ResponseWriter, r *http.Request) {
	type info struct {
		Status string   `json:"status"`
		Addrs  []string `json:"multiaddrs"`
	}
	var list []string = make([]string, 0)
	for _, a := range d.h.Addrs() {
		list = append(list, fmt.Sprintf("%s/p2p/%s", a.String(), d.h.ID().String()))
	}
	resp := info{
		Status: "ok",
		Addrs:  list,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
