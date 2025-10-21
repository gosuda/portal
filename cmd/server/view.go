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

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(ctx context.Context, addr string, d *relaydns.RelayServer, h host.Host, cancel context.CancelFunc) *http.Server {
	mux := http.NewServeMux()

	// Index page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		rows := make([]relaydns.AdminRow, 0)
		for _, v := range d.Hosts() {
			ttl := ""
			if v.Info.TTL > 0 {
				ttl = fmt.Sprintf("%ds", v.Info.TTL)
			}
			// Derive a friendly kind from protocol id. Be lenient to variations.
			p := strings.ToLower(v.Info.Proto)
			kind := "TCP"
			if strings.Contains(p, "ssh") {
				kind = "SSH"
			} else if strings.Contains(p, "http") {
				kind = "HTTP"
			}
			rows = append(rows, relaydns.AdminRow{
				Peer:      v.Info.Peer,
				Name:      v.Info.Name,
				DNS:       v.Info.DNS,
				LastSeen:  time.Since(v.LastSeen).Round(time.Second).String() + " ago",
				Link:      "/peer/" + v.Info.Peer + "/",
				TTL:       ttl,
				Connected: v.Connected,
				Kind:      kind,
			})
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		log.Debug().Int("clients", len(rows)).Msg("render admin index")
		_ = adminIndexTmpl.Execute(w, relaydns.AdminPage{
			NodeID: h.ID().String(),
			Addrs:  relaydns.BuildAddrs(h),
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

	// JSON hosts (namespaced snapshot with server peer and connected peers only)
	mux.HandleFunc("/hosts", func(w http.ResponseWriter, r *http.Request) {
		list := d.Hosts()
		peers := make([]string, 0, len(list))
		for _, v := range list {
			if v.Connected {
				peers = append(peers, v.Info.Peer)
			}
		}
		snap := relaydns.Hosts{
			ServerPeer:  h.ID().String(),
			ServerAddrs: relaydns.BuildAddrs(h),
			Peers:       peers,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snap)
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		type info struct {
			Status string   `json:"status"`
			Addrs  []string `json:"multiaddrs"`
		}
		resp := info{Status: "ok", Addrs: relaydns.BuildAddrs(h)}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		log.Info().Msgf("[server] http: %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("[server] http error")
			cancel()
		}
	}()

	return srv
}

var adminIndexTmpl = template.Must(template.New("admin-index").Parse(`<!doctype html>
<html lang="ko">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>RelayDNS — Admin</title>
  <style>
    * { box-sizing: border-box }
    :root {
      --bg:#fafbff; --panel:#ffffff; --ink:#0f172a; --muted:#6b7280; --line:#e9eef5;
      --primary:#2563eb; --ok:#059669; --bad:#b91c1c; --ok-bg:#ecfdf5; --bad-bg:#fee2e2;
    }
    body { margin:0; background:var(--bg); color:var(--ink); font-family:sans-serif; font-size:16px; line-height:1.6 }
    .wrap { max-width: 980px; margin: 0 auto; padding: 32px 20px }
    header { display:flex; align-items:center; justify-content:space-between; padding: 20px 24px; background:var(--panel); border:1px solid var(--line); border-radius: 14px }
    .brand { font-weight:800; font-size:22px; letter-spacing:.2px }
    .status { color:var(--ok); font-weight:700 }
    main { margin-top: 22px }
    .section { background:var(--panel); border:1px solid var(--line); border-radius:14px; padding:18px; margin-bottom:14px }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 13px; color:#374151; word-break: break-all }
    .title { font-weight:800; margin:0 0 10px 0; font-size:18px }
    .muted { color:var(--muted); font-size:14px }
    .pill { display:inline-flex; align-items:center; gap:8px; padding:6px 10px; border-radius:999px; font-weight:800; font-size:13px }
    .pill.ok { background:var(--ok-bg); color:var(--ok) }
    .pill.bad { background:var(--bad-bg); color:var(--bad) }
    .pill .dot { width:8px; height:8px; border-radius:999px; background:var(--ok); display:inline-block }
    .pill.bad .dot { background:var(--bad) }
    .head { display:flex; align-items:center; justify-content:space-between; gap:12px }
    .btn { display:inline-block; background:var(--primary); color:#fff; text-decoration:none; border-radius:10px; padding:10px 14px; font-weight:800; margin-top:8px }
  </style>
  </head>
<body>
  <div class="wrap">
    <header>
      <div class="brand">RelayDNS</div>
      <div class="status">Admin</div>
    </header>
    <main>
      <section class="section">
        <div class="title">Server</div>
        <div class="mono">Peer ID: {{.NodeID}}</div>
        {{if .Addrs}}
          <div class="muted" style="margin-top:6px">Multiaddrs</div>
          <div class="mono">{{range .Addrs}}{{.}}<br/>{{end}}</div>
        {{end}}
        <div class="muted" style="margin-top:6px">Known clients: {{len .Rows}}</div>
      </section>
      {{range .Rows}}
      <section class="section">
        <div class="head">
          <div class="title">{{if .Name}}{{.Name}}{{else}}(unnamed){{end}}</div>
          <div>
            <span class="muted" style="margin-right:8px">{{.Kind}}</span>
            {{if .Connected}}
              <span class="pill ok"><span class="dot"></span>Connected</span>
            {{else}}
              <span class="pill bad"><span class="dot"></span>Disconnected</span>
            {{end}}
          </div>
        </div>
        {{if .DNS}}<div class="muted">DNS: <span class="mono">{{.DNS}}</span></div>{{end}}
        <div class="muted">Peer</div>
        <div class="mono">{{.Peer}}</div>
        <div class="muted" style="margin-top:6px">Last seen: {{.LastSeen}}{{if .TTL}} · TTL: {{.TTL}}{{end}}</div>
        <a class="btn" href="{{.Link}}">Open</a>
      </section>
      {{else}}
      <section class="section">
        <div class="title">No clients discovered</div>
        <div class="muted">Start a client and ensure bootstraps are configured.</div>
      </section>
      {{end}}
    </main>
  </div>
</body>
</html>`))
