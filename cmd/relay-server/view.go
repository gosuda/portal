package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	pathpkg "path"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/utils/wsstream"
	"gosuda.org/portal/sdk"
)

func serveAsset(mux *http.ServeMux, route, assetPath, contentType string) {
	mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {

		fullPath := pathpkg.Join("app", assetPath)
		b, err := appFS.ReadFile(fullPath)
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

	// Create app UI mux
	appMux := http.NewServeMux()

	// Serve embedded favicons (ico/png/svg) for portal UI
	serveAsset(appMux, "/favicon.ico", "/favicon.ico", "image/x-icon")
	serveAsset(appMux, "/favicon.png", "/favicon.png", "image/png")
	serveAsset(appMux, "/favicon.svg", "/favicon.svg", "image/svg+xml")

	// Portal app assets (JS, CSS, etc.) - served from /app/
	appMux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/app/")
		serveAppStatic(w, r, path, serv)
	})

	// Portal frontend files (for unified caching)
	appMux.HandleFunc("/frontend/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/frontend/")

		// Special handling for manifest.json - generate dynamically
		if path == "manifest.json" {
			serveDynamicManifest(w)
			return
		}

		servePortalStaticFile(w, r, path)
	})

	appMux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
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

	// App UI index page - serve React frontend with SSR (delegates to serveAppStatic)
	appMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// serveAppStatic handles both "/" and 404 fallback with SSR
		path := strings.TrimPrefix(r.URL.Path, "/")
		serveAppStatic(w, r, path, serv)
	})

	appMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{\"status\":\"ok\"}"))
	})

	// Create portal frontend mux
	portalMux := createPortalMux()

	// Top-level handler that routes based on host and path
	topHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPortalSubdomain(r.Host) {
			portalMux.ServeHTTP(w, r)
		} else {
			appMux.ServeHTTP(w, r)
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
	Hide        bool
	Metadata    string
}

// convertLeaseEntriesToRows converts LeaseEntry data from LeaseManager to leaseRow format for the app page
func convertLeaseEntriesToRows(serv *portal.RelayServer) []leaseRow {
	// Get all lease entries directly from the lease manager
	leaseEntries := serv.GetAllLeaseEntries()

	// Initialize with empty slice instead of nil to avoid "null" in JSON
	rows := []leaseRow{}
	now := time.Now()

	for _, leaseEntry := range leaseEntries {
		// Check if lease is still valid
		if now.After(leaseEntry.Expires) {
			continue
		}

		lease := leaseEntry.Lease
		identityID := string(lease.Identity.Id)

		// Metadata parsing
		var meta sdk.Metadata
		_ = json.Unmarshal([]byte(lease.Metadata), &meta)
		if meta.Hide {
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

		// Use frontend pattern if available, otherwise fall back to portalHost
		var link string
		if portalFrontendPattern != "" {
			// For wildcard patterns like *.localhost:4017, replace * with lease name
			if strings.HasPrefix(portalFrontendPattern, "*.") {
				link = fmt.Sprintf("//%s%s", lease.Name, strings.TrimPrefix(portalFrontendPattern, "*"))
			} else {
				// For non-wildcard patterns, construct URL with lease name as subdomain
				link = fmt.Sprintf("//%s.%s/", lease.Name, portalFrontendPattern)
			}
		} else {
			link = fmt.Sprintf("//%s.%s/", lease.Name, portalHost)
		}

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
			Hide:        meta.Hide,
			Metadata:    lease.Metadata,
		}

		if row.Hide != true {
			rows = append(rows, row)
		}
	}

	return rows
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}
