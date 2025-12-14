package main

import (
	"context"
	"embed"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/utils"
)

//go:embed dist/*
var distFS embed.FS

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(addr string, serv *portal.RelayServer, admin *Admin, frontend *Frontend, noIndex bool, cancel context.CancelFunc) *http.Server {
	if addr == "" {
		addr = ":0"
	}

	// Initialize WASM cache used by content handlers
	if err := frontend.InitWasmCache(); err != nil {
		log.Error().Err(err).Msg("failed to initialize WASM cache")
	}

	// Create app UI mux
	appMux := http.NewServeMux()

	// Serve favicons (ico/png/svg) from dist/app
	frontend.ServeAsset(appMux, "/favicon.ico", "favicon.ico", "image/x-icon")
	frontend.ServeAsset(appMux, "/favicon.png", "favicon.png", "image/png")
	frontend.ServeAsset(appMux, "/favicon.svg", "favicon.svg", "image/svg+xml")

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
	}

	// Regular static file serving
	w.Header().Set("Cache-Control", "public, max-age=3600")
	serveStaticFile(w, r, filePath, "")
}

// serveAppStatic serves static files for app UI (React app) from embedded FS
// Falls back to portal.html with SSR when path is root or file not found
func serveAppStatic(w http.ResponseWriter, r *http.Request, appPath string, serv *portal.RelayServer) {
	// Prevent directory traversal
	if strings.Contains(appPath, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	utils.SetCORSHeaders(w)

	// If path is empty or "/", serve portal.html with SSR
	if appPath == "" || appPath == "/" {
		servePortalHTMLWithSSR(w, r, serv)
		return
	}

	// Try to read from embedded FS
	fullPath := path.Join("dist", "app", appPath)
	data, err := distFS.ReadFile(fullPath)
	if err != nil {
		// File not found - fallback to portal.html with SSR for SPA routing
		log.Debug().Err(err).Str("path", appPath).Msg("app static file not found, falling back to SSR")
		servePortalHTMLWithSSR(w, r, serv)
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

// wasmCache stores pre-loaded WASM files in memory (optional)
type wasmCacheEntry struct {
	brotli []byte
	raw    []byte
	hash   string
}

var (
	wasmCache   = make(map[string]*wasmCacheEntry)
	wasmCacheMu sync.RWMutex
)

// initWasmCache loads pre-built WASM artifacts (precompressed) into memory on startup.
func initWasmCache() error {
	// Read all files in embedded dist/wasm directory
	entries, err := distFS.ReadDir("dist/wasm")
	if err != nil {
		return err
	}

	// Portal frontend files (for unified caching)
	appMux.HandleFunc("/frontend/", func(w http.ResponseWriter, r *http.Request) {
		utils.SetCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/frontend/")
		if p == "manifest.json" {
			frontend.ServeDynamicManifest(w, r)
			return
		}

	// Load precompressed variant (brotli) from embed.FS (<hash>.wasm.br)
	var brData []byte
	data, err := distFS.ReadFile(fullPath)
	if err != nil {
		log.Warn().Err(err).Str("file", fullPath).Msg("failed to read brotli-compressed WASM")
	} else {
		brData = data
	}

	// Load uncompressed WASM (<hash>.wasm) for clients without Brotli support
	var rawData []byte
	rawPath := strings.TrimSuffix(fullPath, ".br")
	if rawPath != fullPath {
		if data, err := distFS.ReadFile(rawPath); err == nil {
			rawData = data
		} else {
			log.Warn().Err(err).Str("file", rawPath).Msg("failed to read uncompressed WASM")
		}
	}

	entry := &wasmCacheEntry{
		brotli: brData,
		raw:    rawData,
		hash:   hashHex,
	}

	wasmCacheMu.Lock()
	wasmCache[name] = entry
	wasmCacheMu.Unlock()

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

		// Check if IP is banned
		clientIP := manager.ExtractClientIP(r)
		ipManager := admin.GetIPManager()
		if ipManager != nil && ipManager.IsIPBanned(clientIP) {
			log.Warn().Str("ip", clientIP).Msg("[server] connection rejected: IP banned")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		stream, wsConn, err := utils.UpgradeToWSStream(w, r, nil)
		if err != nil {
			log.Error().Err(err).Msg("[server] websocket upgrade failed")
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
	w.Header().Set("Content-Type", "application/wasm")

	// Check Accept-Encoding header for brotli support
	acceptEncoding := strings.ToLower(r.Header.Get("Accept-Encoding"))
	hasBrotli := strings.Contains(acceptEncoding, "br") && len(entry.brotli) > 0

	switch {
	case hasBrotli:
		w.Header().Set("Content-Encoding", "br")
		w.Header().Set("Content-Length", strconv.Itoa(len(entry.brotli)))
		w.WriteHeader(http.StatusOK)
		w.Write(entry.brotli)
		log.Debug().
			Str("path", filePath).
			Int("size", len(entry.brotli)).
			Str("encoding", "brotli").
			Msg("served compressed WASM")
	case len(entry.raw) > 0:
		w.Header().Del("Content-Encoding")
		w.Header().Set("Content-Length", strconv.Itoa(len(entry.raw)))
		w.WriteHeader(http.StatusOK)
		w.Write(entry.raw)
		log.Warn().
			Str("path", filePath).
			Str("acceptEncoding", acceptEncoding).
			Int("size", len(entry.raw)).
			Msg("client missing brotli support, served uncompressed WASM")
	default:
		fullPath := path.Join("dist", "wasm", filePath)
		if data, err := distFS.ReadFile(fullPath); err == nil {
			w.Header().Del("Content-Encoding")
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			w.Write(data)
			log.Warn().
				Str("path", filePath).
				Msg("fallback served WASM from embedded FS without cache entry")
			return
		}

		log.Error().
			Str("path", filePath).
			Msg("no WASM asset available to serve")
		http.Error(w, "WASM asset unavailable", http.StatusInternalServerError)
	}
}

// servePortalStatic serves static files for portal frontend with appropriate cache headers
// Falls back to portal.html for SPA routing (404 -> portal.html)
func servePortalStatic(w http.ResponseWriter, r *http.Request) {
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
		serveDynamicManifest(w, r)
		return

	case "service-worker.js":
		serveDynamicServiceWorker(w, r)
		return

	case "wasm_exec.js":
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Content-Type", "application/javascript")
		serveStaticFileWithFallback(w, r, staticPath, "application/javascript")
		return

	case "portal.mp4":
		w.Header().Set("Cache-Control", "public, max-age=604800")
		w.Header().Set("Content-Type", "video/mp4")
		serveStaticFileWithFallback(w, r, staticPath, "video/mp4")
		return
	}

	// Default caching for other files
	w.Header().Set("Cache-Control", "public, max-age=3600")
	serveStaticFileWithFallback(w, r, staticPath, "")
}

// serveStaticFile reads and serves a file from the static directory
func serveStaticFile(w http.ResponseWriter, r *http.Request, filePath string, contentType string) {
	utils.SetCORSHeaders(w)

	fullPath := path.Join("dist", "wasm", filePath)
	data, err := distFS.ReadFile(fullPath)
	if err != nil {
		log.Debug().Err(err).Str("path", filePath).Msg("static file not found")
		http.NotFound(w, r)
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
		frontend.ServeAppStatic(w, r, p, serv)
	})

	appMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{\"status\":\"ok\"}"))
	})

	// Admin API
	appMux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		admin.HandleAdminRequest(w, r, serv)
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
			frontend.ServeDynamicManifest(w, r)
			return
		}
		frontend.ServePortalStaticFile(w, r, p)
	})

	// Service worker for portal subdomains (serve from dist/wasm)
	portalMux.HandleFunc("/service-worker.js", func(w http.ResponseWriter, r *http.Request) {
		frontend.ServeDynamicServiceWorker(w, r)
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
			frontend.ServeStaticFile(w, r, "portal.html", "text/html; charset=utf-8")
			return
		}
		frontend.ServePortalStatic(w, r)
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
	Peer         string
	Name         string
	Kind         string
	Connected    bool
	DNS          string
	LastSeen     string
	LastSeenISO  string
	FirstSeenISO string
	TTL          string
	Link         string
	StaleRed     bool
	Hide         bool
	Metadata     string
	BPS          int64  // bytes-per-second limit (0 = unlimited)
	IsApproved   bool   // whether lease is approved (for manual mode)
	IsDenied     bool   // whether lease is denied (for manual mode)
	IP           string // client IP address (for IP-based ban)
	IsIPBanned   bool   // whether the IP is banned
}
