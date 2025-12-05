package main

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/sdk"
	"gosuda.org/portal/utils"
)

//go:embed dist/*
var distFS embed.FS

// Package-level BPS manager reference for admin handlers
var globalBPSManager *BPSManager

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(addr string, serv *portal.RelayServer, bpsManager *BPSManager, nodeID string, bootstraps []string, noIndex bool, cancel context.CancelFunc) *http.Server {
	globalBPSManager = bpsManager
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

	if noIndex {
		appMux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("User-agent: *\nDisallow: /\n"))
		})
	}

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

	// Tunnel installer script and binaries
	appMux.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		serveTunnelScript(w, r)
	})
	appMux.HandleFunc("/tunnel/bin/", func(w http.ResponseWriter, r *http.Request) {
		serveTunnelBinary(w, r)
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

	// Admin API
	appMux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		handleAdminRequest(w, r, serv)
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
		if utils.IsSubdomain(flagPortalAppURL, r.Host) {
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
	BPS         int64 // bytes-per-second limit (0 = unlimited)
}

// convertLeaseEntriesToRows converts LeaseEntry data from LeaseManager to leaseRow format for the app page
func handleAdminRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer) {
	if !isLocalhost(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Simple routing for admin
	p := strings.TrimPrefix(r.URL.Path, "/admin/")

	if p == "leases" && r.Method == http.MethodGet {
		// List all leases in the same format as SSR data (leaseRow)
		rows := convertLeaseEntriesToAdminRows(serv)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(rows)
		return
	}

	if p == "stats" && r.Method == http.MethodGet {
		// Basic stats
		stats := map[string]interface{}{
			"leases_count": len(serv.GetAllLeaseEntries()),
			"uptime":       "TODO", // We could add start time to server
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
		return
	}

	if strings.HasPrefix(p, "leases/") && strings.HasSuffix(p, "/ban") {
		parts := strings.Split(p, "/")
		if len(parts) == 3 {
			encodedID := parts[1]

			// Decode ID (expecting URL-safe base64 from frontend)
			idBytes, err := base64.URLEncoding.DecodeString(encodedID)
			if err != nil {
				// Try Raw URL encoding
				idBytes, err = base64.RawURLEncoding.DecodeString(encodedID)
			}

			leaseID := encodedID
			if err == nil {
				leaseID = string(idBytes)
			}

			if r.Method == http.MethodPost {
				serv.GetLeaseManager().BanLease(leaseID)
				saveAdminSettings(serv, globalBPSManager)
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.Method == http.MethodDelete {
				serv.GetLeaseManager().UnbanLease(leaseID)
				saveAdminSettings(serv, globalBPSManager)
				w.WriteHeader(http.StatusOK)
				return
			}
		}
	}

	if p == "leases/banned" && r.Method == http.MethodGet {
		banned := serv.GetLeaseManager().GetBannedLeases()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(banned)
		return
	}

	// Set BPS limit for a lease: POST/DELETE /admin/leases/{id}/bps
	if strings.HasPrefix(p, "leases/") && strings.HasSuffix(p, "/bps") {
		parts := strings.Split(p, "/")
		if len(parts) == 3 {
			encodedID := parts[1]

			// Decode ID (expecting URL-safe base64 from frontend)
			idBytes, err := base64.URLEncoding.DecodeString(encodedID)
			if err != nil {
				idBytes, err = base64.RawURLEncoding.DecodeString(encodedID)
			}

			leaseID := encodedID
			if err == nil {
				leaseID = string(idBytes)
			}

			if r.Method == http.MethodPost {
				var req struct {
					BPS int64 `json:"bps"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "Invalid request body", http.StatusBadRequest)
					return
				}
				oldBPS := globalBPSManager.GetBPSLimit(leaseID)
				globalBPSManager.SetBPSLimit(leaseID, req.BPS)
				log.Info().
					Str("lease_id", leaseID).
					Int64("old_bps", oldBPS).
					Int64("new_bps", req.BPS).
					Msg("[Admin] BPS limit updated")
				saveAdminSettings(serv, globalBPSManager)
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.Method == http.MethodDelete {
				oldBPS := globalBPSManager.GetBPSLimit(leaseID)
				globalBPSManager.SetBPSLimit(leaseID, 0)
				log.Info().
					Str("lease_id", leaseID).
					Int64("old_bps", oldBPS).
					Msg("[Admin] BPS limit removed (now unlimited)")
				saveAdminSettings(serv, globalBPSManager)
				w.WriteHeader(http.StatusOK)
				return
			}
		}
	}

	serveAppStatic(w, r, "", serv)
}

// convertLeaseEntriesToAdminRows converts LeaseEntry data to leaseRow format for admin API
// Unlike convertLeaseEntriesToRows, this includes banned and hidden entries
func convertLeaseEntriesToAdminRows(serv *portal.RelayServer) []leaseRow {
	leaseEntries := serv.GetAllLeaseEntries()
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

		// Format last active as relative time
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

		// Create DNS label from identity
		dnsLabel := identityID
		if len(dnsLabel) > 8 {
			dnsLabel = dnsLabel[:8] + "..."
		}

		// Build link using the configured subdomain base
		base := flagPortalAppURL
		if base == "" {
			base = flagPortalURL
		}
		link := fmt.Sprintf("//%s.%s/", lease.Name, utils.StripWildCard(utils.StripScheme(base)))

		// Get BPS limit for this lease from BPSManager
		bps := globalBPSManager.GetBPSLimit(identityID)

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
			BPS:         bps,
		}

		rows = append(rows, row)
	}

	return rows
}

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

		// Skip banned leases for user-facing list
		if isLeaseBanned(serv, identityID) {
			continue
		}

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
		base := flagPortalAppURL
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

func isLocalhost(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host == "127.0.0.1" || host == "::1"
}

// isLeaseBanned checks if a lease ID is in the banned list
func isLeaseBanned(serv *portal.RelayServer, leaseID string) bool {
	bannedList := serv.GetLeaseManager().GetBannedLeases()
	for _, banned := range bannedList {
		bannedStr := string(banned)
		log.Debug().
			Str("checking_lease", leaseID).
			Str("banned_entry", bannedStr).
			Bool("match", bannedStr == leaseID).
			Msg("[BanCheck] Comparing lease IDs")
		if bannedStr == leaseID {
			return true
		}
	}
	return false
}

// AdminSettings stores persistent admin configuration
type AdminSettings struct {
	BannedLeases []string         `json:"banned_leases"`
	BPSLimits    map[string]int64 `json:"bps_limits"`
}

var (
	adminSettingsPath = "admin_settings.json"
	adminSettingsMu   sync.Mutex
)

// SetAdminSettingsPath sets the path for admin settings file
func SetAdminSettingsPath(path string) {
	adminSettingsMu.Lock()
	defer adminSettingsMu.Unlock()
	adminSettingsPath = path
}

// saveAdminSettings persists ban and BPS settings to disk
func saveAdminSettings(serv *portal.RelayServer, bpsManager *BPSManager) {
	adminSettingsMu.Lock()
	defer adminSettingsMu.Unlock()

	lm := serv.GetLeaseManager()

	// Collect banned leases
	bannedBytes := lm.GetBannedLeases()
	banned := make([]string, len(bannedBytes))
	for i, b := range bannedBytes {
		banned[i] = string(b)
	}

	// Collect BPS limits from BPSManager
	bpsLimits := bpsManager.GetAllBPSLimits()

	settings := AdminSettings{
		BannedLeases: banned,
		BPSLimits:    bpsLimits,
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("[Admin] Failed to marshal admin settings")
		return
	}

	// Ensure directory exists
	dir := filepath.Dir(adminSettingsPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Error().Err(err).Msg("[Admin] Failed to create settings directory")
			return
		}
	}

	if err := os.WriteFile(adminSettingsPath, data, 0644); err != nil {
		log.Error().Err(err).Msg("[Admin] Failed to save admin settings")
		return
	}

	log.Debug().Str("path", adminSettingsPath).Msg("[Admin] Saved admin settings")
}

// loadAdminSettings loads ban and BPS settings from disk
func loadAdminSettings(serv *portal.RelayServer, bpsManager *BPSManager) {
	adminSettingsMu.Lock()
	defer adminSettingsMu.Unlock()

	data, err := os.ReadFile(adminSettingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug().Msg("[Admin] No admin settings file found, starting fresh")
			return
		}
		log.Error().Err(err).Msg("[Admin] Failed to read admin settings")
		return
	}

	var settings AdminSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		log.Error().Err(err).Msg("[Admin] Failed to parse admin settings")
		return
	}

	lm := serv.GetLeaseManager()

	// Restore banned leases
	for _, leaseID := range settings.BannedLeases {
		lm.BanLease(leaseID)
	}

	// Restore BPS limits to BPSManager
	for leaseID, bps := range settings.BPSLimits {
		bpsManager.SetBPSLimit(leaseID, bps)
	}

	log.Info().
		Int("banned_count", len(settings.BannedLeases)).
		Int("bps_limits_count", len(settings.BPSLimits)).
		Msg("[Admin] Loaded admin settings")
}
