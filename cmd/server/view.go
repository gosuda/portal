package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/gosuda/relaydns/relaydns"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/rs/zerolog/log"
)

// serveHTTP builds the HTTP mux and starts serving admin UI + per-peer proxy.
func serveHTTP(ctx context.Context, addr string, d *relaydns.Director, h host.Host, cancel context.CancelFunc) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()

	// Index page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		rows := make([]row, 0)
		for _, v := range d.Hosts() {
			ttl := ""
			if v.Info.TTL > 0 {
				ttl = fmt.Sprintf("%ds", v.Info.TTL)
			}
			rows = append(rows, row{
				Peer:      v.Info.Peer,
				Name:      v.Info.Name,
				DNS:       v.Info.DNS,
				LastSeen:  time.Since(v.LastSeen).Round(time.Second).String() + " ago",
				Link:      "/peer/" + v.Info.Peer + "/",
				TTL:       ttl,
				Connected: v.Connected,
			})
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		log.Debug().Int("clients", len(rows)).Msg("render admin index")
		_ = adminIndexTmpl.Execute(w, page{
			NodeID: h.ID().String(),
			Addrs:  buildAddrs(h),
			Rows:   rows,
		})
	})

	// Per-peer proxy
	mux.HandleFunc("/peer/", func(w http.ResponseWriter, r *http.Request) {
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
		d.ProxyHTTP(w, r, peerID, pathSuffix)
	})

	// JSON hosts
	mux.HandleFunc("/hosts", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(d.Hosts())
	})

	// Health
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		type info struct {
			Status string   `json:"status"`
			Addrs  []string `json:"multiaddrs"`
		}
		resp := info{Status: "ok", Addrs: buildAddrs(h)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	log.Info().Msgf("[server] http (admin+proxy): %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Error().Err(err).Msg("[server] http error")
		cancel()
	}
}

// view model types used by template rendering
type row struct {
	Peer      string
	Name      string
	DNS       string
	LastSeen  string
	Link      string
	TTL       string
	Connected bool
}

type page struct {
	NodeID string
	Addrs  []string
	Rows   []row
}

func buildAddrs(h host.Host) []string {
	out := make([]string, 0)
	for _, a := range h.Addrs() {
		out = append(out, fmt.Sprintf("%s/p2p/%s", a.String(), h.ID().String()))
	}
	return out
}

var adminIndexTmpl = template.Must(template.New("admin-index").Parse(`<!doctype html>
<html lang="ko">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>RelayDNS — Admin</title>
  <style>
    * { box-sizing: border-box }
    body {
      margin: 0;
      background: #f6f7fb;
      color: #111827;
      font-family: sans-serif;
      font-size: 16px;
      line-height: 1.5;
    }
    .wrap { max-width: 960px; margin: 0 auto; padding: 28px 18px }
    header { display:flex; align-items:center; justify-content:space-between; padding: 14px 18px; background:#ffffff; border:1px solid #e5e7eb; border-radius: 10px }
    .brand { font-weight: 700; font-size: 20px }
    .status { color:#059669; font-weight:600 }
    main { margin-top: 18px; display:block }
    .box { background:#ffffff; border:1px solid #e5e7eb; border-radius:10px; padding:16px; margin-bottom:12px }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 14px; color:#374151; word-break: break-all }
    .title { font-weight:700; margin: 0 0 8px 0; font-size: 18px; color:#374151 }
    .muted { color:#6b7280; font-size: 14px }
    .btn { display:inline-block; background:#2563eb; color:#fff; text-decoration:none; border-radius:8px; padding:10px 14px; font-weight:700; margin-top: 6px }
    .stat { display:inline-flex; align-items:center; gap:8px; padding:6px 10px; border-radius:999px; font-weight:700; font-size:14px }
    .stat.connected { background:#ecfdf5; color:#065f46 }
    .stat.disconnected { background:#fee2e2; color:#b91c1c }
    .stat .dot { width:8px; height:8px; border-radius:999px; background:#10b981; display:inline-block }
    .stat.disconnected .dot { background:#ef4444 }
    .head { display:flex; align-items:center; justify-content:space-between; gap:12px }
  </style>
  </head>
<body>
  <div class="wrap">
    <header>
      <div class="brand">RelayDNS Admin</div>
      <div class="status">Active</div>
    </header>
    <main>
      <section class="box">
        <div class="title">Server</div>
        <div class="mono">Peer ID: {{.NodeID}}</div>
        {{if .Addrs}}
          <div class="muted" style="margin-top:6px">Multiaddrs</div>
          <div class="mono">{{range .Addrs}}{{.}}<br/>{{end}}</div>
        {{end}}
        <div class="muted" style="margin-top:6px">Known clients: {{len .Rows}}</div>
      </section>
      {{range .Rows}}
      <section class="box">
        <div class="head">
          <div class="title">{{if .Name}}{{.Name}}{{else}}(unnamed){{end}}</div>
          {{if .Connected}}
            <span class="stat connected"><span class="dot"></span>Connected</span>
          {{else}}
            <span class="stat disconnected"><span class="dot"></span>Disconnected</span>
          {{end}}
        </div>
        {{if .DNS}}<div class="muted">DNS: <span class="mono">{{.DNS}}</span></div>{{end}}
        <div class="muted">Peer</div>
        <div class="mono">{{.Peer}}</div>
        <div class="muted" style="margin-top:6px">Last seen: {{.LastSeen}}{{if .TTL}} · TTL: {{.TTL}}{{end}}</div>
        <a class="btn" href="{{.Link}}">Open</a>
      </section>
      {{else}}
      <section class="box">
        <div class="title">No clients discovered</div>
        <div class="muted">Start a client and ensure bootstraps are configured.</div>
      </section>
      {{end}}
    </main>
  </div>
</body>
</html>`))
