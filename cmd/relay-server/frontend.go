package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/sdk"
	"gosuda.org/portal/utils"
)

type readDirFileFS interface {
	fs.ReadFileFS
	fs.ReadDirFS
}

// Frontend handles serving embedded frontend assets and SSR.
type Frontend struct {
	distFS readDirFileFS
	admin  *Admin

	cachedPortalHTML     []byte
	cachedPortalHTMLOnce sync.Once

	wasmCache   map[string]*wasmCacheEntry
	wasmCacheMu sync.RWMutex
}

func NewFrontend() *Frontend {
	return &Frontend{
		distFS:    distFS,
		wasmCache: make(map[string]*wasmCacheEntry),
	}
}

// SetAdmin attaches an Admin instance. Frontend methods tolerate nil admin.
func (f *Frontend) SetAdmin(admin *Admin) {
	f.admin = admin
}

func (f *Frontend) initPortalHTMLCache() error {
	var err error
	f.cachedPortalHTML, err = f.distFS.ReadFile("dist/app/portal.html")
	return err
}

func (f *Frontend) ServeAsset(mux *http.ServeMux, route, assetPath, contentType string) {
	mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
		fullPath := path.Join("dist", "app", assetPath)
		b, err := f.distFS.ReadFile(fullPath)
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

// servePortalHTMLWithSSR serves portal.html with SSR data injection.
func (f *Frontend) servePortalHTMLWithSSR(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer) {
	utils.SetCORSHeaders(w)

	// Initialize cache on first use
	f.cachedPortalHTMLOnce.Do(func() {
		if err := f.initPortalHTMLCache(); err != nil {
			log.Error().Err(err).Msg("Failed to cache portal.html")
		}
	})

	if f.cachedPortalHTML == nil {
		http.NotFound(w, r)
		return
	}

	// Inject SSR data into cached template
	injectedHTML := f.injectServerData(string(f.cachedPortalHTML), serv)

	// Set headers
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")

	// Send response
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(injectedHTML))

	log.Debug().Msg("Served portal.html with SSR data")
}

// injectServerData injects server data into HTML for SSR
func (f *Frontend) injectServerData(htmlContent string, serv *portal.RelayServer) string {
	// Get server data from lease manager
	rows := []leaseRow{}
	if f.admin != nil {
		rows = convertLeaseEntriesToRows(serv, f.admin)
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(rows)
	if err != nil {
		log.Error().Err(err).Msg("Failed to marshal server data for SSR")
		jsonData = []byte("[]")
	}

	// Create SSR script tag
	ssrScript := `<script id="__SSR_DATA__" type="application/json">` + string(jsonData) + `</script>`

	// Inject before </head> tag
	injected := strings.Replace(htmlContent, "</head>", ssrScript+"\n</head>", 1)

	log.Debug().
		Int("rows", len(rows)).
		Int("jsonSize", len(jsonData)).
		Msg("Injected SSR data into HTML")

	return injected
}

// convertLeaseEntriesToRows converts LeaseEntry data from LeaseManager to leaseRow format for the app page.
func convertLeaseEntriesToRows(serv *portal.RelayServer, admin *Admin) []leaseRow {
	leaseEntries := serv.GetAllLeaseEntries()
	rows := []leaseRow{}
	now := time.Now()

	bannedList := serv.GetLeaseManager().GetBannedLeases()
	bannedMap := make(map[string]struct{}, len(bannedList))
	for _, b := range bannedList {
		bannedMap[string(b)] = struct{}{}
	}

	for _, leaseEntry := range leaseEntries {
		if now.After(leaseEntry.Expires) {
			continue
		}

		lease := leaseEntry.Lease
		identityID := string(lease.Identity.Id)

		var metadata sdk.Metadata
		_ = json.Unmarshal([]byte(lease.Metadata), &metadata)

		if _, banned := bannedMap[identityID]; banned {
			continue
		}

		if admin != nil {
			approveManager := admin.GetApproveManager()
			if approveManager.GetApprovalMode() == manager.ApprovalModeManual && !approveManager.IsLeaseApproved(identityID) {
				continue
			}
		}

		if metadata.Hide {
			continue
		}

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
			return fmt.Sprintf("%ds", int(d/time.Second))
		}(since)
		lastSeenISO := leaseEntry.LastSeen.UTC().Format(time.RFC3339)
		firstSeenISO := leaseEntry.FirstSeen.UTC().Format(time.RFC3339)

		connected := serv.IsConnectionActive(leaseEntry.ConnectionID)

		if !connected && since >= 3*time.Minute {
			continue
		}

		name := lease.Name
		if name == "" {
			name = "(unnamed)"
		}

		kind := "client"
		if len(lease.Alpn) > 0 {
			kind = lease.Alpn[0]
		}

		dnsLabel := identityID
		if len(dnsLabel) > 8 {
			dnsLabel = dnsLabel[:8] + "..."
		}

		base := flagPortalAppURL
		if base == "" {
			base = flagPortalURL
		}
		link := fmt.Sprintf("//%s.%s/", lease.Name, utils.StripWildCard(utils.StripScheme(base)))

		var bps int64
		if bpsMgr := admin.GetBPSManager(); bpsMgr != nil {
			bps = bpsMgr.GetBPSLimit(identityID)
		}

		row := leaseRow{
			Peer:         identityID,
			Name:         name,
			Kind:         kind,
			Connected:    connected,
			DNS:          dnsLabel,
			LastSeen:     lastSeenStr,
			LastSeenISO:  lastSeenISO,
			FirstSeenISO: firstSeenISO,
			TTL:          ttlStr,
			Link:         link,
			StaleRed:     !connected && since >= 15*time.Second,
			Hide:         leaseEntry.ParsedMetadata != nil && leaseEntry.ParsedMetadata.Hide,
			Metadata:     lease.Metadata,
			BPS:          bps,
		}

		if !metadata.Hide {
			rows = append(rows, row)
		}
	}

	return rows
}

// ServePortalStaticFile serves static files for portal frontend with caching.
func (f *Frontend) ServePortalStaticFile(w http.ResponseWriter, r *http.Request, filePath string) {
	// Check if this is a content-addressed WASM file
	if strings.HasSuffix(filePath, ".wasm") {
		hash := strings.TrimSuffix(filePath, ".wasm")
		if utils.IsHexString(hash) {
			f.serveCompressedWasm(w, r, filePath)
			return
		}
	}

	// Regular static file serving
	w.Header().Set("Cache-Control", "public, max-age=3600")
	f.ServeStaticFile(w, r, filePath, "")
}

// ServeAppStatic serves static files for app UI (React app) from embedded FS.
// Falls back to portal.html with SSR when path is root or file not found.
func (f *Frontend) ServeAppStatic(w http.ResponseWriter, r *http.Request, appPath string, serv *portal.RelayServer) {
	// Prevent directory traversal
	if strings.Contains(appPath, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	utils.SetCORSHeaders(w)

	// If path is empty or "/", serve portal.html with SSR
	if appPath == "" || appPath == "/" {
		f.servePortalHTMLWithSSR(w, r, serv)
		return
	}

	// Try to read from embedded FS
	fullPath := path.Join("dist", "app", appPath)
	data, err := f.distFS.ReadFile(fullPath)
	if err != nil {
		// File not found - fallback to portal.html with SSR for SPA routing
		log.Debug().Err(err).Str("path", appPath).Msg("app static file not found, falling back to SSR")
		f.servePortalHTMLWithSSR(w, r, serv)
		return
	}

	// Set content type based on extension
	ext := path.Ext(appPath)
	contentType := utils.GetContentType(ext)
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	w.Write(data)

	log.Debug().
		Str("path", appPath).
		Int("size", len(data)).
		Msg("served app static file")
}

type wasmCacheEntry struct {
	brotli []byte
	hash   string
}

// initWasmCache loads pre-built WASM artifacts (precompressed) into memory on startup.
func (f *Frontend) InitWasmCache() error {
	// Read all files in embedded dist/wasm directory
	entries, err := f.distFS.ReadDir("dist/wasm")
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Look for content-addressed WASM files: <hex>.wasm.br
		if strings.HasSuffix(name, ".wasm.br") {
			hash := strings.TrimSuffix(name, ".wasm.br")
			if utils.IsHexString(hash) {
				fullPath := path.Join("dist", "wasm", name)
				// Cache under the URL path (<hash>.wasm) while reading the
				// compressed artifacts from embed.FS.
				cacheKey := hash + ".wasm"
				if err := f.cacheWasmFile(cacheKey, fullPath); err != nil {
					log.Warn().Err(err).Str("file", name).Msg("failed to cache WASM file")
				} else {
					log.Info().Str("file", cacheKey).Msg("cached WASM file")
				}
			}
		}
	}

	return nil
}

// cacheWasmFile reads and caches a WASM file and its pre-compressed variant (brotli).
func (f *Frontend) cacheWasmFile(name, fullPath string) error {
	// Verify name looks like a hex hash (name is <hash>.wasm).
	hashHex := strings.TrimSuffix(name, ".wasm")
	if !utils.IsHexString(hashHex) {
		log.Warn().Str("file", name).Msg("WASM file name is not a valid SHA256 hex string")
	}

	// Load brotli variant (<hash>.wasm.br)
	var brData []byte
	brPath := fullPath
	if !strings.HasSuffix(brPath, ".br") {
		brPath += ".br" // Should be implied by caller logic, but safe
	}

	data, err := f.distFS.ReadFile(brPath)
	if err != nil {
		log.Warn().Err(err).Str("file", brPath).Msg("failed to read brotli-compressed WASM")
	} else {
		brData = data
	}

	entry := &wasmCacheEntry{
		brotli: brData,
		hash:   hashHex,
	}

	f.wasmCacheMu.Lock()
	f.wasmCache[name] = entry
	f.wasmCacheMu.Unlock()

	log.Debug().
		Str("file", name).
		Int("brotli", len(entry.brotli)).
		Msg("WASM file cached")

	return nil
}

// serveCompressedWasm serves pre-compressed WASM files from memory cache
func (f *Frontend) serveCompressedWasm(w http.ResponseWriter, r *http.Request, filePath string) {
	f.wasmCacheMu.RLock()
	entry, ok := f.wasmCache[filePath]
	f.wasmCacheMu.RUnlock()

	if !ok {
		log.Debug().Str("path", filePath).Msg("WASM file not in cache")
		// Fallback: try to serve uncompressed WASM from embedded FS
		fullPath := path.Join("dist", "wasm", filePath)
		data, err := f.distFS.ReadFile(fullPath)
		if err != nil {
			log.Debug().Err(err).Str("path", fullPath).Msg("WASM file not found in embedded FS")
			http.NotFound(w, r)
			return
		}

		// Serve uncompressed WASM
		utils.SetCORSHeaders(w)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("Content-Type", "application/wasm")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		w.Write(data)
		log.Debug().
			Str("path", filePath).
			Int("size", len(data)).
			Msg("served uncompressed WASM from embedded FS")
		return
	}

	// Set immutable cache headers for content-addressed files
	utils.SetCORSHeaders(w)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("Content-Type", "application/wasm")

	// Check Accept-Encoding header for brotli support
	// Check Accept-Encoding header
	acceptEncoding := r.Header.Get("Accept-Encoding")

	// 1. Brotli
	if strings.Contains(acceptEncoding, "br") && len(entry.brotli) > 0 {
		w.Header().Set("Content-Encoding", "br")
		w.Header().Set("Content-Length", strconv.Itoa(len(entry.brotli)))
		w.WriteHeader(http.StatusOK)
		w.Write(entry.brotli)
		log.Debug().
			Str("path", filePath).
			Int("size", len(entry.brotli)).
			Str("encoding", "brotli").
			Msg("served compressed WASM")
		return
	}

	// 2. Fallback to uncompressed WASM from embedded FS.
	// This ensures TinyGo WASM works even when clients/proxies don't advertise brotli.
	log.Warn().
		Str("path", filePath).
		Str("acceptEncoding", acceptEncoding).
		Msg("falling back to uncompressed WASM")

	fullPath := path.Join("dist", "wasm", filePath)
	data, err := f.distFS.ReadFile(fullPath)
	if err != nil {
		log.Error().Err(err).Str("path", fullPath).Msg("uncompressed WASM not found in embedded FS")
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	w.Write(data)
	log.Debug().
		Str("path", filePath).
		Int("size", len(data)).
		Str("encoding", "identity").
		Msg("served uncompressed WASM")
	return
}

// ServePortalStatic serves static files for portal frontend with appropriate cache headers.
// Falls back to portal.html for SPA routing (404 -> portal.html).
func (f *Frontend) ServePortalStatic(w http.ResponseWriter, r *http.Request) {
	staticPath := strings.TrimPrefix(r.URL.Path, "/")

	// Prevent directory traversal
	if strings.Contains(staticPath, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	// Special handling for specific files
	switch staticPath {
	case "manifest.json":
		// Serve dynamic manifest regardless of static presence
		f.ServeDynamicManifest(w, r)
		return

	case "service-worker.js":
		f.ServeDynamicServiceWorker(w, r)
		return

	case "wasm_exec.js":
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Content-Type", "application/javascript")
		f.serveStaticFileWithFallback(w, r, staticPath, "application/javascript")
		return

	case "portal.mp4":
		w.Header().Set("Cache-Control", "public, max-age=604800")
		w.Header().Set("Content-Type", "video/mp4")
		f.serveStaticFileWithFallback(w, r, staticPath, "video/mp4")
		return
	}

	// Default caching for other files
	w.Header().Set("Cache-Control", "public, max-age=3600")
	f.serveStaticFileWithFallback(w, r, staticPath, "")
}

// ServeStaticFile reads and serves a file from the static directory.
func (f *Frontend) ServeStaticFile(w http.ResponseWriter, r *http.Request, filePath string, contentType string) {
	utils.SetCORSHeaders(w)

	fullPath := path.Join("dist", "wasm", filePath)
	data, err := f.distFS.ReadFile(fullPath)
	if err != nil {
		log.Debug().Err(err).Str("path", filePath).Msg("static file not found")
		http.NotFound(w, r)
		return
	}

	// Set content type
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		ext := path.Ext(filePath)
		ct := utils.GetContentType(ext)
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
	}

	log.Debug().
		Str("path", filePath).
		Int("size", len(data)).
		Msg("served static file")

	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// serveStaticFileWithFallback reads and serves a file from the static directory
// If the file is not found, it falls back to portal.html for SPA routing
func (f *Frontend) serveStaticFileWithFallback(w http.ResponseWriter, r *http.Request, filePath string, contentType string) {
	utils.SetCORSHeaders(w)

	fullPath := path.Join("dist", "wasm", filePath)
	data, err := f.distFS.ReadFile(fullPath)
	if err != nil {
		// File not found - fallback to portal.html for SPA routing
		log.Debug().Err(err).Str("path", filePath).Msg("static file not found, serving portal.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		f.ServeStaticFile(w, r, "portal.html", "text/html; charset=utf-8")
		return
	}

	// Set content type
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		ext := path.Ext(filePath)
		ct := utils.GetContentType(ext)
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
	}

	log.Debug().
		Str("path", filePath).
		Int("size", len(data)).
		Msg("served static file")

	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// serveDynamicManifest generates and serves manifest.json dynamically
func (f *Frontend) ServeDynamicManifest(w http.ResponseWriter, _ *http.Request) {
	utils.SetCORSHeaders(w)

	// Find the content-addressed WASM file
	f.wasmCacheMu.RLock()
	var wasmHash string
	var wasmFile string
	for filename, entry := range f.wasmCache {
		wasmHash = entry.hash
		wasmFile = filename
		break // Use the first (and should be only) WASM file
	}
	f.wasmCacheMu.RUnlock()

	// Fallback: scan embedded WASM directory if cache is empty
	if wasmHash == "" {
		entries, err := f.distFS.ReadDir("dist/wasm")
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				name := entry.Name()
				// Look for content-addressed WASM files: <hex>.wasm.br
				if strings.HasSuffix(name, ".wasm.br") {
					hash := strings.TrimSuffix(name, ".wasm.br")
					if utils.IsHexString(hash) {
						wasmHash = hash
						wasmFile = hash + ".wasm"
						break
					}
				}
			}
		}
	}

	// Generate WASM URL
	wasmURL := flagPortalURL + "/frontend/" + wasmFile

	// Create manifest structure
	manifest := map[string]string{
		"wasmFile":   wasmFile,
		"wasmUrl":    wasmURL,
		"hash":       wasmHash,
		"bootstraps": strings.Join(flagBootstraps, ","),
	}

	// Set headers for no caching
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "application/json")

	// Encode and send
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(manifest); err != nil {
		log.Error().Err(err).Msg("Failed to encode manifest")
	}

	log.Debug().
		Str("wasmFile", wasmFile).
		Str("wasmUrl", wasmURL).
		Str("hash", wasmHash).
		Str("bootstraps", strings.Join(flagBootstraps, ",")).
		Msg("Served dynamic manifest")
}

// ServeDynamicServiceWorker serves service-worker.js with injected manifest and config.
func (f *Frontend) ServeDynamicServiceWorker(w http.ResponseWriter, r *http.Request) {
	utils.SetCORSHeaders(w)

	// Read the service-worker.js template
	fullPath := path.Join("dist", "wasm", "service-worker.js")
	content, err := f.distFS.ReadFile(fullPath)
	if err != nil {
		log.Error().Err(err).Msg("Failed to read service-worker.js")
		http.NotFound(w, r)
		return
	}

	// Set headers for no caching
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "application/javascript")

	// Send response
	w.WriteHeader(http.StatusOK)
	w.Write(content)

	log.Debug().Msg("Served service-worker.js")
}
