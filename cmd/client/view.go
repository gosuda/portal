package main

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"

	"github.com/rs/zerolog/log"
)

type clientViewData struct {
	NodeID     string
	Name       string
	ALPNs      []string
	Bootstraps []string
	Relays     []string
}

// serveClientHTTP starts a tiny admin UI for the client.
func serveClientHTTP(ctx context.Context, addr string, nodeID string, name string, alpns []string, bootstraps []string, getRelays func() []string, cancel context.CancelFunc) *http.Server {
	if addr == "" {
		addr = ":0"
	}

	mux := http.NewServeMux()

	// Index page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		data := clientViewData{
			NodeID:     nodeID,
			Name:       name,
			ALPNs:      alpns,
			Bootstraps: bootstraps,
			Relays:     getRelays(),
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := clientTmpl.Execute(w, data); err != nil {
			log.Error().Err(err).Msg("[client] render admin index")
		}
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		type info struct {
			Status string `json:"status"`
		}
		_ = json.NewEncoder(w).Encode(info{Status: "ok"})
	})

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		log.Info().Msgf("[client] admin http: %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("[client] admin http error")
			cancel()
		}
	}()
	return srv
}

var clientTmpl = template.Must(template.New("client-index").Parse(`<!doctype html>
<html lang="ko">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>RelayDNS Client</title>
  <style>
    * { box-sizing: border-box }
    :root {
      --bg:#fafbff; --panel:#ffffff; --ink:#0f172a; --muted:#6b7280; --line:#e9eef5;
      --primary:#2563eb; --ok:#059669; --bad:#b91c1c; --ok-bg:#ecfdf5; --bad-bg:#fee2e2;
    }
    body { margin:0; background:var(--bg); color:var(--ink); font-family:sans-serif; font-size:16px; line-height:1.6 }
    .wrap { max-width: 760px; margin: 0 auto; padding: 28px 18px }
    header { display:flex; align-items:center; justify-content:space-between; padding: 16px 20px; background:var(--panel); border:1px solid var(--line); border-radius: 12px }
    .brand { font-weight:800; font-size:20px; letter-spacing:.2px }
    main { margin-top: 18px }
    .section { background:var(--panel); border:1px solid var(--line); border-radius:12px; padding:16px; margin-bottom:12px }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 13px; color:#374151; word-break: break-all }
    .title { font-weight:800; margin:0 0 8px 0; font-size:17px }
    .muted { color:var(--muted); font-size:14px }
  </style>
  </head>
<body>
  <div class="wrap">
    <header>
      <div class="brand">RelayDNS Client</div>
      <div class="muted">Admin</div>
    </header>
    <main>
      <section class="section">
        <div class="title">Client</div>
        <div class="muted">Lease ID</div>
        <div class="mono">{{.NodeID}}</div>
        <div class="muted" style="margin-top:6px">Name</div>
        <div class="mono">{{.Name}}</div>
        <div class="muted" style="margin-top:6px">ALPNs</div>
        <div class="mono">{{range .ALPNs}}{{.}}<br/>{{end}}</div>
      </section>
      <section class="section">
        <div class="title">Bootstraps</div>
        {{if .Bootstraps}}
          <div class="mono">{{range .Bootstraps}}{{.}}<br/>{{end}}</div>
        {{else}}
          <div class="muted">No bootstrap URLs configured.</div>
        {{end}}
      </section>
      <section class="section">
        <div class="title">Connected Relays</div>
        {{if .Relays}}
          <div class="mono">{{range .Relays}}{{.}}<br/>{{end}}</div>
        {{else}}
          <div class="muted">No relays connected.</div>
        {{end}}
      </section>
    </main>
  </div>
</body>
</html>`))
