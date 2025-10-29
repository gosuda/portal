package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/relaydns/relaydns"
	"github.com/gosuda/relaydns/relaydns/utils/wsstream"
	"github.com/gosuda/relaydns/sdk"
)

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(_ context.Context, addr string, serv *relaydns.RelayServer, nodeID string, bootstraps []string, cancel context.CancelFunc) *http.Server {
	if addr == "" {
		addr = ":0"
	}

	mux := http.NewServeMux()

	// Per-peer HTTP reverse proxy over RelayDNS
	// Route: /peer/{leaseID}/*
	var (
		proxyClient     *sdk.RDClient
		proxyClientOnce sync.Once
		proxyClientErr  error
	)
	// Lazily initialize a client that connects to provided bootstraps or current server
	initProxyClient := func(r *http.Request) (*sdk.RDClient, error) {
		proxyClientOnce.Do(func() {
			bs := bootstraps
			if len(bs) == 0 {
				// Derive bootstrap from current request host
				// Assume same host/port as admin with path /relay
				scheme := "ws"
				// No TLS handling here; extend to wss if needed in future
				bs = []string{fmt.Sprintf("%s://%s/relay", scheme, r.Host)}
			}
			proxyClient, proxyClientErr = sdk.NewClient(func(c *sdk.RDClientConfig) {
				c.BootstrapServers = bs
			})
		})
		return proxyClient, proxyClientErr
	}

	mux.HandleFunc("/peer/", func(w http.ResponseWriter, r *http.Request) {
		// Expect path /peer/{nameOrID}[/{rest}]
		path := strings.TrimPrefix(r.URL.Path, "/peer/")
		if path == "" {
			http.NotFound(w, r)
			return
		}
		parts := strings.SplitN(path, "/", 2)
		nameOrID := parts[0]
		rest := "/"
		if len(parts) == 2 && parts[1] != "" {
			rest = "/" + parts[1]
		}

		// Redirect /peer/my-title to /peer/my-title/ for proper relative path resolution
		// This ensures that relative paths like "style.css" resolve to "/peer/my-title/style.css"
		// instead of "/peer/style.css"
		if len(parts) == 1 && !strings.HasSuffix(r.URL.Path, "/") {
			redirectURL := r.URL.Path + "/"
			if r.URL.RawQuery != "" {
				redirectURL += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, redirectURL, http.StatusMovedPermanently)
			return
		}

		// URL decode the name (handles Unicode like 한글 → %ED%95%9C%EA%B8%80)
		decodedName, err := url.QueryUnescape(nameOrID)
		if err != nil {
			log.Warn().Err(err).Str("name", nameOrID).Msg("[server] Failed to decode peer name")
			decodedName = nameOrID // Fallback to original if decode fails
		}

		// Try to find lease by name first, then by ID
		leaseID := ""
		leaseEntries := serv.GetAllLeaseEntries()
		for _, entry := range leaseEntries {
			if entry.Lease.Name == decodedName {
				leaseID = string(entry.Lease.Identity.Id)
				break
			}
		}
		// If not found by name, assume it's an ID
		if leaseID == "" {
			leaseID = decodedName
		}

		// Get ALPN from lease metadata
		alpns := serv.GetLeaseALPNs(leaseID)
		if len(alpns) == 0 {
			http.Error(w, "lease not found or no ALPN registered", http.StatusNotFound)
			return
		}

		if !slices.Contains(alpns, "http/1.1") {
			http.Error(w, "no http/1.1 ALPN registered", http.StatusNotFound)
			return
		}

		// Temporary credential for this proxy connection
		cred := sdk.NewCredential()

		client, err := initProxyClient(r)
		if err != nil {
			http.Error(w, "proxy client init: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Create a reverse proxy whose transport dials via SDK client
		target, _ := url.Parse("http://relay-peer")
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Transport = &http.Transport{
			DialContext: func(c context.Context, network, address string) (net.Conn, error) {
				conn, err := client.Dial(cred, leaseID, "http/1.1")
				if err != nil {
					return nil, err
				}
				return conn, nil
			},
		}

		proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, e error) {
			log.Error().Err(e).Str("lease", leaseID).Msg("[server] proxy error")
			http.Error(rw, "upstream error", http.StatusBadGateway)
		}

		// Use default Director; adjust path on a shallow clone
		r2 := r.Clone(r.Context())
		r2.URL.Path = rest
		proxy.ServeHTTP(w, r2)
	})

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

		// Convert lease entries to rows for the admin page
		rows := convertLeaseEntriesToRows(serv)

		data := adminPageData{
			NodeID:     nodeID,
			Bootstraps: bootstraps,
			Rows:       rows,
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		log.Debug().Msg("render admin index")
		if err := serverTmpl.Execute(w, data); err != nil {
			log.Error().Err(err).Msg("[server] render admin index")
		}
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
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

// convertLeaseEntriesToRows converts LeaseEntry data from LeaseManager to leaseRow format for the admin page
func convertLeaseEntriesToRows(serv *relaydns.RelayServer) []leaseRow {
	// Get all lease entries directly from the lease manager
	leaseEntries := serv.GetAllLeaseEntries()

	var rows []leaseRow
	now := time.Now()

	for _, leaseEntry := range leaseEntries {
		// Check if lease is still valid
		if now.After(leaseEntry.Expires) {
			continue
		}

		lease := leaseEntry.Lease
		identityID := string(lease.Identity.Id)

		// Calculate TTL
		ttl := time.Until(leaseEntry.Expires)
		ttlStr := ""
		if ttl > 0 {
			if ttl > time.Hour {
				ttlStr = fmt.Sprintf("%.0fh", ttl.Hours())
			} else if ttl > time.Minute {
				ttlStr = fmt.Sprintf("%.0fm", ttl.Minutes())
			} else {
				ttlStr = fmt.Sprintf("%.0fs", ttl.Seconds())
			}
		}

		// Format last seen time
		lastSeenStr := leaseEntry.LastSeen.Format("2006-01-02 15:04:05")

		// Check if connection is still active by checking if the connection ID exists in the connections map
		connected := serv.IsConnectionActive(leaseEntry.ConnectionID)

		// Use name from lease if available
		name := lease.Name
		if name == "" {
			name = "(unnamed)"
		}

		// Determine kind/type based on ALPN if available
		kind := "client"
		if len(lease.Alpn) > 0 {
			kind = lease.Alpn[0]
		}

		// Create DNS label from identity (first 8 chars for display)
		dnsLabel := identityID
		if len(dnsLabel) > 8 {
			dnsLabel = dnsLabel[:8] + "..."
		}

		// Create link for the lease
		// Use name if available, otherwise fall back to identity ID
		linkPath := name
		if linkPath == "" || linkPath == "(unnamed)" {
			linkPath = identityID
		}
		link := fmt.Sprintf("/peer/%s", linkPath)

		row := leaseRow{
			Peer:      identityID,
			Name:      name,
			Kind:      kind,
			Connected: connected,
			DNS:       dnsLabel,
			LastSeen:  lastSeenStr,
			TTL:       ttlStr,
			Link:      link,
		}

		rows = append(rows, row)
	}

	return rows
}

var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
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
