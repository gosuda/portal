package main

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/relaydns/relaydns"
	"github.com/gosuda/relaydns/relaydns/utils/wsstream"
)

type leaseRow struct {
	Peer      string
	Name      string
	Kind      string
	Connected bool
	DNS       string
	LastSeen  string
	TTL       string
	Link      string
}

type adminPageData struct {
	NodeID     string
	Bootstraps []string
	Rows       []leaseRow
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(ctx context.Context, addr string, serv *relaydns.RelayServer, nodeID string, bootstraps []string, cancel context.CancelFunc) *http.Server {
	if addr == "" {
		addr = ":0"
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		wsConn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Error().Err(err).Msg("[server] websocket upgrade failed")
			return
		}

		stream := &wsstream.WsStream{Conn: wsConn}
		if err := serv.HandleConnection(stream); err != nil {
			log.Error().Err(err).Msg("[server] websocket relay connection error")
			wsConn.Close()
			return
		}
	})

	// Index page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		data := adminPageData{
			NodeID:     nodeID,
			Bootstraps: bootstraps,
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		log.Debug().Msg("render admin index")
		if err := serverTmpl.Execute(w, data); err != nil {
			log.Error().Err(err).Msg("[server] render admin index")
		}
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		type info struct {
			Status string `json:"status"`
		}
		resp := info{Status: "ok"}
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

var serverTmpl = template.Must(template.New("admin-index").Parse(`<!doctype html>
<html lang="ko">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>RelayDNS Admin</title>
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
        <div class="mono">Server ID: {{.NodeID}}</div>
        {{if .Bootstraps}}
          <div class="muted" style="margin-top:6px">Bootstrap URLs</div>
          <div class="mono">{{range .Bootstraps}}<a href="{{.}}" target="_blank" rel="noreferrer noopener">{{.}}</a><br/>{{end}}</div>
        {{end}}
        <div class="muted" style="margin-top:6px">Active clients: {{len .Rows}}</div>
      </section>
      {{range .Rows}}
      <section class="section" id="peer-{{.Peer}}" data-peer="{{.Peer}}" data-name="{{.Name}}">
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
        {{if .DNS}}<div class="muted">DNS Label: <span class="mono">{{.DNS}}</span></div>{{end}}
        <div class="muted">Lease Identity</div>
        <div class="mono">{{.Peer}}</div>
        <div class="muted" style="margin-top:6px">Last seen: {{.LastSeen}}{{if .TTL}} - TTL: {{.TTL}}{{end}}</div>
        <a class="btn" href="{{.Link}}">Open</a>
      </section>
      {{else}}
      <section class="section">
        <div class="title">No clients discovered</div>
        <div class="muted">Start a client and ensure bootstrap URLs point at this server's /relay WebSocket endpoint.</div>
      </section>
      {{end}}
    </main>
  </div>
</body>
</html>`))
