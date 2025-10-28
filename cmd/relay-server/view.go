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
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/relaydns/relaydns"
	"github.com/gosuda/relaydns/relaydns/utils/wsstream"
	"github.com/gosuda/relaydns/sdk"
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
		link := fmt.Sprintf("/peer/%s", identityID)

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

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(ctx context.Context, addr string, serv *relaydns.RelayServer, nodeID string, bootstraps []string, alpn string, cancel context.CancelFunc) *http.Server {
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
	// Lazily initialize a client that connects to provided bootstraps or the current server
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
		// Expect path /peer/{leaseID}[/{rest}]
		path := strings.TrimPrefix(r.URL.Path, "/peer/")
		if path == "" {
			http.NotFound(w, r)
			return
		}
		// Split leaseID and remainder
		var leaseID, rest string
		slash := strings.IndexByte(path, '/')
		if slash == -1 {
			leaseID, rest = path, "/"
		} else {
			leaseID, rest = path[:slash], path[slash:]
			if rest == "" {
				rest = "/"
			}
		}

		client, err := initProxyClient(r)
		if err != nil {
			http.Error(w, "proxy init failed", http.StatusBadGateway)
			log.Error().Err(err).Msg("[server] init proxy client")
			return
		}

		// Create a reverse proxy whose transport dials via RelayDNS to the lease
		target, _ := url.Parse("http://relay-peer")
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Transport = &http.Transport{
			DialContext: func(c context.Context, network, address string) (net.Conn, error) {
				// Create a fresh credential per dial
				cred, cerr := sdk.NewCredential()
				if cerr != nil {
					return nil, cerr
				}
				return client.Dial(cred, leaseID, alpn)
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

	mux.HandleFunc("/api/leases", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Convert lease entries to rows
		rows := convertLeaseEntriesToRows(serv)

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(rows); err != nil {
			log.Error().Err(err).Msg("[server] failed to encode lease data")
			http.Error(w, "internal server error", http.StatusInternalServerError)
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
  <script>
    // Auto-refresh lease data every 5 seconds
    async function refreshLeases() {
      try {
        const response = await fetch('/api/leases');
        if (!response.ok) throw new Error('Failed to fetch leases');
        
        const leases = await response.json();
        updateLeaseDisplay(leases);
      } catch (error) {
        console.error('Error refreshing leases:', error);
      }
    }
    
    function updateLeaseDisplay(leases) {
      const main = document.querySelector('main');
      
      // Find the existing sections
      const serverSection = main.querySelector('.section');
      const existingLeaseSections = main.querySelectorAll('.section[id^="peer-"]');
      const noClientsSection = main.querySelector('.section:not([id])');
      
      // Remove existing lease sections and "no clients" section
      existingLeaseSections.forEach(section => section.remove());
      if (noClientsSection && noClientsSection.textContent.includes('No clients discovered')) {
        noClientsSection.remove();
      }
      
      // Add lease sections
      if (leases.length === 0) {
        const noClientsSection = document.createElement('section');
        noClientsSection.className = 'section';
        noClientsSection.innerHTML = '<div class="title">No clients discovered</div><div class="muted">Start a client and ensure bootstrap URLs point at this server\'s /relay WebSocket endpoint.</div>';
        main.appendChild(noClientsSection);
      } else {
        leases.forEach(lease => {
          const section = document.createElement('section');
          section.className = 'section';
          section.id = 'peer-' + lease.Peer;
          section.setAttribute('data-peer', lease.Peer);
          section.setAttribute('data-name', lease.Name);
          
          const connectedClass = lease.Connected ? 'ok' : 'bad';
          const connectedText = lease.Connected ? 'Connected' : 'Disconnected';
          const displayName = lease.Name || '(unnamed)';
          
          let html = '<div class="head"><div class="title">' + displayName + '</div><div><span class="muted" style="margin-right:8px">' + lease.Kind + '</span><span class="pill ' + connectedClass + '"><span class="dot"></span>' + connectedText + '</span></div></div>';
          if (lease.DNS) {
            html += '<div class="muted">DNS Label: <span class="mono">' + lease.DNS + '</span></div>';
          }
          html += '<div class="muted">Lease Identity</div><div class="mono">' + lease.Peer + '</div><div class="muted" style="margin-top:6px">Last seen: ' + lease.LastSeen;
          if (lease.TTL) {
            html += ' - TTL: ' + lease.TTL;
          }
          html += '</div><a class="btn" href="' + lease.Link + '">Open</a>';
          section.innerHTML = html;
          
          main.appendChild(section);
        });
      }
      
      // Update active clients count
      const activeCountElement = serverSection.querySelector('.muted');
      if (activeCountElement && activeCountElement.textContent.includes('Active clients:')) {
        activeCountElement.textContent = 'Active clients: ' + leases.length;
      }
    }
    
    // Start auto-refresh when page loads
    document.addEventListener('DOMContentLoaded', () => {
      // Initial refresh
      refreshLeases();
      
      // Set up interval for auto-refresh
      setInterval(refreshLeases, 5000);
    });
  </script>
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
