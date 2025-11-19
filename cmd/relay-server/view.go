package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/sdk"
	"gosuda.org/portal/utils"
)

//go:embed dist/*
var distFS embed.FS

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(addr string, serv *portal.RelayServer, nodeID string, bootstraps []string, cancel context.CancelFunc) *http.Server {
	if addr == "" {
		addr = ":0"
	}

	// Initialize WASM cache used by content handlers
	if err := initWasmCache(); err != nil {
		log.Error().Err(err).Msg("failed to initialize WASM cache")
	}

	// Create app UI mux
	appMux := http.NewServeMux()

	// Serve favicons (ico/png/svg) from dist/app
	serveAsset(appMux, "/favicon.ico", "favicon.ico", "image/x-icon")
	serveAsset(appMux, "/favicon.png", "favicon.png", "image/png")
	serveAsset(appMux, "/favicon.svg", "favicon.svg", "image/svg+xml")

	// Portal app assets (JS, CSS, etc.) - served from /app/
	appMux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		utils.SetCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/app/")
		serveAppStatic(w, r, p, serv)
	})

	// Portal frontend files (for unified caching)
	appMux.HandleFunc("/frontend/", func(w http.ResponseWriter, r *http.Request) {
		utils.SetCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/frontend/")
		if p == "manifest.json" {
			serveDynamicManifest(w, r)
			return
		}

		servePortalStaticFile(w, r, p)
	})

	appMux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		stream, wsConn, err := utils.UpgradeToWSStream(w, r, nil)
		if err != nil {
			log.Error().Err(err).Msg("[server] websocket upgrade failed")
			return
		}
		if err := serv.HandleConnection(stream); err != nil {
			log.Error().Err(err).Msg("[server] websocket relay connection error")
			wsConn.Close()
			return
		}
	})

	// App UI index page - serve React frontend with SSR (delegates to serveAppStatic)
	appMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// serveAppStatic handles both "/" and 404 fallback with SSR
		p := strings.TrimPrefix(r.URL.Path, "/")
		serveAppStatic(w, r, p, serv)
	})

	appMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{\"status\":\"ok\"}"))
	})

	// Create portal frontend mux (routes only)
	portalMux := http.NewServeMux()

	// Static file handler for /frontend/ (for unified caching)
	portalMux.HandleFunc("/frontend/", func(w http.ResponseWriter, r *http.Request) {
		utils.SetCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/frontend/")
		if p == "manifest.json" {
			serveDynamicManifest(w, r)
			return
		}
		servePortalStaticFile(w, r, p)
	})

	// Service worker for portal subdomains (serve from dist/wasm)
	portalMux.HandleFunc("/service-worker.js", func(w http.ResponseWriter, r *http.Request) {
		serveDynamicServiceWorker(w, r)
	})

	// Root and SPA fallback for portal subdomains
	portalMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		utils.SetCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/" {
			// Serve portal HTML from dist/wasm
			serveStaticFile(w, r, "portal.html", "text/html; charset=utf-8")
			return
		}
		servePortalStatic(w, r)
	})

	// routes based on host and path
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Route subdomain requests (e.g., *.example.com) to portalMux
		// and everything else to the app UI mux.
		if utils.IsSubdomain(flagPortalSubdomainURL, r.Host) {
			portalMux.ServeHTTP(w, r)
		} else {
			appMux.ServeHTTP(w, r)
		}
	})

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
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

		// Build link using the configured subdomain base
		base := flagPortalSubdomainURL
		if base == "" {
			base = flagPortalURL
		}
		link := fmt.Sprintf("//%s.%s/", lease.Name, utils.StripWildCard(utils.StripScheme(base)))

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
