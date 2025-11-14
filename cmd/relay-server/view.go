package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/utils/wsstream"
)

//go:embed static
var assetsFS embed.FS

func serveAsset(mux *http.ServeMux, route, assetPath, contentType string) {
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

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(_ context.Context, addr string, serv *portal.RelayServer, nodeID string, bootstraps []string, cancel context.CancelFunc) *http.Server {
	if addr == "" {
		addr = ":0"
	}

	// Parse template once per server instance (no global var)
	tmpl := template.Must(template.ParseFS(assetsFS, "static/index.html"))

	// Create admin UI mux
	adminMux := http.NewServeMux()

	// Serve embedded favicons (ico/png/svg) for admin UI
	serveAsset(adminMux, "/favicon.ico", "static/favicon/favicon.ico", "image/x-icon")
	serveAsset(adminMux, "/favicon.png", "static/favicon/favicon.png", "image/png")
	serveAsset(adminMux, "/favicon.svg", "static/favicon/favicon.svg", "image/svg+xml")

	// Static assets for admin UI (embedded files)
	adminMux.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/static/")
		serveAdminStatic(w, r, path)
	})

	// Portal frontend files (for unified caching)
	adminMux.HandleFunc("/frontend/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/frontend/")

		// Special handling for manifest.json - generate dynamically
		if path == "manifest.json" {
			serveDynamicManifest(w, r)
			return
		}

		servePortalStaticFile(w, r, path)
	})

	adminMux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
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

	// Admin UI index page
	adminMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
		if err := tmpl.Execute(w, data); err != nil {
			log.Error().Err(err).Msg("[server] error rendering admin index")
		}
	})

	adminMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{\"status\":\"ok\"}"))
	})

	// Create portal frontend mux
	portalMux := createPortalMux()

	// Top-level handler that routes based on host
	topHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPortalSubdomain(r.Host) {
			portalMux.ServeHTTP(w, r)
		} else {
			adminMux.ServeHTTP(w, r)
		}
	})

	srv := &http.Server{
		Addr:    addr,
		Handler: topHandler,
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

		// Metadata parsing and hide check
		var meta struct {
			Hide bool `json:"hide"`
		}
		if err := json.Unmarshal([]byte(lease.Metadata), &meta); err == nil && meta.Hide {
			continue
		}

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

		link := fmt.Sprintf("//%s.%s/", lease.Name, portalHost)

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
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}
