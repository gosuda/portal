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

	"github.com/gosuda/portal/portal"
	"github.com/gosuda/portal/portal/utils/wsstream"
	"github.com/gosuda/portal/sdk"
)

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(_ context.Context, addr string, serv *portal.RelayServer, nodeID string, bootstraps []string, cancel context.CancelFunc) *http.Server {
	if addr == "" {
		addr = ":0"
	}

	mux := http.NewServeMux()

	// Per-peer HTTP reverse proxy over Portal
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
			proxyClient, proxyClientErr = sdk.NewClient(sdk.WithBootstrapServers(bs))
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
func convertLeaseEntriesToRows(serv *portal.RelayServer) []leaseRow {
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
  <title>Portal Admin</title>
  <style>
    * { box-sizing: border-box }
    :root {
      --bg:#fafbff; --panel:#ffffff; --ink:#0f172a; --muted:#6b7280; --line:#e9eef5;
      --primary:#2563eb; --ok:#059669; --bad:#b91c1c; --ok-bg:#ecfdf5; --bad-bg:#fee2e2;
    }
    body { margin:0; background:var(--bg); color:var(--ink); font-family:sans-serif; font-size:16px; line-height:1.6 }
    .wrap { max-width: 980px; margin: 0 auto; padding: 32px 20px }
    header { display:flex; align-items:center; gap:16px; justify-content:space-between; padding: 20px 24px; background:var(--panel); border:1px solid var(--line); border-radius: 14px }
    .brand { display:flex; align-items:center; height:36px; font-weight:800; font-size:18px; letter-spacing:.2px }
    .status { color:var(--ok); font-weight:700 }
    .app-left { display:flex; align-items:center; gap:12px }
    main { margin-top: 22px }
    .section { background:var(--panel); border:1px solid var(--line); border-radius:14px; padding:18px; margin-bottom:14px }
    .grid { display:grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap:16px; margin-top:14px }
    .card { background:var(--panel); border:1px solid var(--line); border-radius:14px; padding:16px; display:flex; flex-direction:column; box-shadow: 0 1px 2px rgba(15,23,42,0.06); transition: box-shadow .2s ease, transform .2s ease; text-decoration:none; color:inherit; cursor:pointer; min-height: 180px }
    .card:hover { box-shadow: 0 6px 20px rgba(15,23,42,0.12); transform: translateY(-2px) }
    .card-head { display:flex; align-items:center; gap:10px; margin-bottom:8px }
    .status-dot { width:10px; height:10px; border-radius:999px; background:var(--muted) }
    .status-dot.ok { background: var(--ok) }
    .status-dot.bad { background: var(--bad) }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 13px; color:#374151; word-break: break-all }
    .title { font-weight:800; margin:0; font-size:16px }
    .muted { color:var(--muted); font-size:13px }
    .pill { display:inline-flex; align-items:center; gap:8px; padding:6px 10px; border-radius:999px; font-weight:800; font-size:13px }
    .pill.ok { background:var(--ok-bg); color:var(--ok) }
    .pill.bad { background:var(--bad-bg); color:var(--bad) }
    .pill .dot { width:8px; height:8px; border-radius:999px; background:var(--ok); display:inline-block }
    .pill.bad .dot { background:var(--bad) }
    .head { display:flex; align-items:center; justify-content:space-between; gap:12px }
    .btn { display:inline-block; background:var(--primary); color:#fff; text-decoration:none; border-radius:8px; padding:6px 10px; font-weight:700; font-size:13px; line-height:1; margin-top:6px }
    .btn.outline { background:transparent; color:var(--primary); border:1px solid var(--primary) }
    .actions { margin-top:auto; display:flex; justify-content:flex-end }
    /* Google-like search bar */
    .searchbar { width: 320px; max-width: 50vw; margin: 0; display:flex; align-items:center; gap:10px; height:36px; padding:0 12px; border:1px solid var(--line); border-radius:999px; background:#fff }
    .searchbar:focus-within { box-shadow: 0 1px 6px rgba(32,33,36,.28); border-color:#dfe1e5 }
    .searchbar input { width:100%; height:100%; border:none; outline:none; font-size:14px; background:transparent; color:#111 }
    .searchbar .icon { width:18px; height:18px; color:#9aa0a6 }
    .topbar { display:flex; align-items:center; gap:16px; justify-content:space-between }
    .rightbar { display:flex; align-items:center; gap:12px; margin-left:auto }
    .gh-btn { width:36px; height:36px; border-radius:999px; display:inline-flex; align-items:center; justify-content:center; color:#000; background:#fff; border:1px solid var(--line) }
    .counter { display:inline-flex; align-items:center; gap:6px; height:36px; padding:0 10px; border-radius:999px; background:var(--ok-bg); color:var(--ok); font-weight:800 }
    .counter .icon { width:18px; height:18px }
  </style>
  </head>
<body>
  <div class="wrap">
    <header>
      <div class="brand">Portal</div>
      <div class="rightbar">
        <div class="searchbar" role="search">
          <svg class="icon" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
            <path d="M15.5 14h-.79l-.28-.27a6.471 6.471 0 0 0 1.57-4.23C15.99 6.01 13.48 3.5 10.49 3.5S5 6.01 5 9s2.51 5.5 5.5 5.5c1.61 0 3.09-.59 4.23-1.57l.27.28v.79l4.25 4.25c.41.41 1.08.41 1.49 0 .41-.41.41-1.08 0-1.49L15.5 14Zm-5 0C8.01 14 6 11.99 6 9.5S8.01 5 10.5 5 15 7.01 15 9.5 12.99 14 10.5 14Z"/>
          </svg>
          <input id="search" type="text" placeholder="Search by name" aria-label="Search by name" oninput="filterCards(this.value)">
        </div>
        <a class="gh-btn" href="https://github.com/gosuda/portal" target="_blank" rel="noopener" aria-label="GitHub repository" title="gosuda/portal">
          <svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
            <path d="M12 .5C5.73.5.5 5.73.5 12c0 5.08 3.29 9.37 7.86 10.88.58.1.79-.25.79-.56 0-.27-.01-1.16-.02-2.11-3.2.69-3.88-1.39-3.88-1.39-.53-1.35-1.29-1.71-1.29-1.71-1.05-.72.08-.7.08-.7 1.16.08 1.78 1.19 1.78 1.19 1.03 1.77 2.7 1.26 3.36.96.1-.75.4-1.26.73-1.55-2.56-.29-5.26-1.28-5.26-5.72 0-1.26.45-2.3 1.19-3.11-.12-.29-.52-1.45.11-3.02 0 0 .98-.31 3.2 1.19.93-.26 1.94-.39 2.94-.39s2.01.13 2.95.39c2.22-1.5 3.2-1.19 3.2-1.19.63 1.57.23 2.73.12 3.02.74.81 1.19 1.85 1.19 3.11 0 4.45-2.7 5.43-5.28 5.72.41.36.77 1.07.77 2.16 0 1.56-.01 2.81-.01 3.19 0 .31.21.67.8.56C20.22 21.36 23.5 17.08 23.5 12 23.5 5.73 18.27.5 12 .5Z"/>
          </svg>
        </a>
        <div class="counter" aria-label="Active clients" title="Active clients">
          <svg class="icon" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
            <path d="M16 11c1.66 0 2.99-1.34 2.99-3S17.66 5 16 5s-3 1.34-3 3 1.34 3 3 3Zm-8 0c1.66 0 2.99-1.34 2.99-3S9.66 5 8 5 5 6.34 5 8s1.34 3 3 3Zm0 2c-2.33 0-7 1.17-7 3.5V19h14v-2.5C18 14.17 13.33 13 11 13Zm8 0c-.29 0-.62.02-.97.05 1.16.84 1.97 1.97 1.97 3.45V19h4v-2.5c0-2.33-4.67-3.5-7-3.5Z"/>
          </svg>
          <span>{{len .Rows}}</span>
        </div>
      </div>
    </header>
    <main>
      

      <div class="grid">
        {{range .Rows}}
        <a class="card {{if .Connected}}connected{{else}}disconnected{{end}}" id="peer-{{.Peer}}" data-peer="{{.Peer}}" data-name="{{.Name}}" href="{{.Link}}">
          <div class="card-head">
            <span class="status-dot {{if .Connected}}ok{{else}}bad{{end}}"></span>
            <div class="title">{{if .Name}}{{.Name}}{{else}}(unnamed){{end}}</div>
          </div>
          <div class="muted">Last seen: {{.LastSeen}}</div>
        </a>
        {{else}}
        <article class="card">
          <div class="title">No clients discovered</div>
          <div class="muted">Start a client and ensure bootstrap URLs point at this server's /relay WebSocket endpoint.</div>
        </article>
        {{end}}
      </div>
    </main>
  </div>
  <script>
    function filterCards(q) {
      q = (q || '').toLowerCase().trim();
      const cards = document.querySelectorAll('.grid .card');
      cards.forEach(card => {
        const name = (card.getAttribute('data-name') || '').toLowerCase();
        const show = !q || name.includes(q);
        card.style.display = show ? '' : 'none';
      });
    }
  </script>
</body>
</html>`))
