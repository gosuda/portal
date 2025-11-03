package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
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

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/utils/wsstream"
	"gosuda.org/portal/sdk"
)

//go:embed static
var assetsFS embed.FS

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(_ context.Context, addr string, serv *portal.RelayServer, nodeID string, bootstraps []string, cancel context.CancelFunc) *http.Server {
	if addr == "" {
		addr = ":0"
	}

	mux := http.NewServeMux()

	// Parse template once per server instance (no global var)
	tmpl := template.Must(template.ParseFS(assetsFS, "static/index.html"))

	// Serve static assets under /static/ (CSS, images)
	if sub, err := fs.Sub(assetsFS, "static"); err == nil {
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	}

	// Serve embedded favicons (ico/png/svg)
	serveAsset := func(route, assetPath, contentType string) {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			b, err := assetsFS.ReadFile(assetPath)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			if contentType != "" {
				w.Header().Set("Content-Type", contentType)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(b)
		})
	}
	serveAsset("/favicon.ico", "static/favicon/favicon.ico", "image/x-icon")
	serveAsset("/favicon.png", "static/favicon/favicon.png", "image/png")
	serveAsset("/favicon.svg", "static/favicon/favicon.svg", "image/svg+xml")

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
		if err := tmpl.Execute(w, data); err != nil {
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
	Peer        string
	Name        string
	Kind        string
	Connected   bool
	DNS         string
	LastSeen    string
	LastSeenISO string
	TTL         string
	Link        string
	StaleRed    bool
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

		// Format last active as relative time (e.g., "1h 4m", "12m 5s", "8s")
		since := now.Sub(leaseEntry.LastSeen)
		if since < 0 {
			since = 0
		}
		lastSeenStr := func(d time.Duration) string {
			if d >= time.Hour {
				h := int(d / time.Hour)
				m := int((d % time.Hour) / time.Minute)
				if m > 0 {
					return fmt.Sprintf("%dh %dm", h, m)
				}
				return fmt.Sprintf("%dh", h)
			}
			if d >= time.Minute {
				m := int(d / time.Minute)
				s := int((d % time.Minute) / time.Second)
				if s > 0 {
					return fmt.Sprintf("%dm %ds", m, s)
				}
				return fmt.Sprintf("%dm", m)
			}
			s := int(d / time.Second)
			return fmt.Sprintf("%ds", s)
		}(since)
		lastSeenISO := leaseEntry.LastSeen.UTC().Format(time.RFC3339)

		// Check if connection is still active
		connected := serv.IsConnectionActive(leaseEntry.ConnectionID)

		// Skip entries that have been disconnected for 3 minutes or more
		if !connected && since >= 3*time.Minute {
			continue
		}

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

		link := fmt.Sprintf("https://%s.portal.gosuda.org/", lease.Name)

		row := leaseRow{
			Peer:        identityID,
			Name:        name,
			Kind:        kind,
			Connected:   connected,
			DNS:         dnsLabel,
			LastSeen:    lastSeenStr,
			LastSeenISO: lastSeenISO,
			TTL:         ttlStr,
			Link:        link,
			StaleRed:    !connected && since >= 15*time.Second,
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
