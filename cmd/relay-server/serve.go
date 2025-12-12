package main

import (
	"encoding/json"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/utils"
)

// Cached portal.html template for efficient SSR
var (
	cachedPortalHTML     []byte
	cachedPortalHTMLOnce sync.Once
)

func initPortalHTMLCache() error {
	var err error
	cachedPortalHTML, err = distFS.ReadFile("dist/app/portal.html")
	return err
}

func serveAsset(mux *http.ServeMux, route, assetPath, contentType string) {
	mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
		// Read from dist/app subdirectory of the embedded FS
		fullPath := path.Join("dist", "app", assetPath)
		b, err := distFS.ReadFile(fullPath)
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

// servePortalHTMLWithSSR serves portal.html with SSR data injection
func servePortalHTMLWithSSR(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, admin *Admin) {
	utils.SetCORSHeaders(w)

	// Initialize cache on first use
	cachedPortalHTMLOnce.Do(func() {
		if err := initPortalHTMLCache(); err != nil {
			log.Error().Err(err).Msg("Failed to cache portal.html")
		}
	})

	if cachedPortalHTML == nil {
		http.NotFound(w, r)
		return
	}

	// Inject SSR data into cached template
	injectedHTML := injectServerData(string(cachedPortalHTML), serv, admin)

	// Set headers
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")

	// Send response
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(injectedHTML))

	log.Debug().Msg("Served portal.html with SSR data")
}

// injectServerData injects server data into HTML for SSR
func injectServerData(htmlContent string, serv *portal.RelayServer, admin *Admin) string {
	// Get server data from lease manager
	rows := convertLeaseEntriesToRows(serv, admin)

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

// servePortalStaticFile serves static files for portal frontend with caching
func servePortalStaticFile(w http.ResponseWriter, r *http.Request, filePath string) {
	// Check if this is a content-addressed WASM file
	if strings.HasSuffix(filePath, ".wasm") {
		hash := strings.TrimSuffix(filePath, ".wasm")
		if utils.IsHexString(hash) {
			serveCompressedWasm(w, r, filePath)
			return
		}
	}

	// Regular static file serving
	w.Header().Set("Cache-Control", "public, max-age=3600")
	serveStaticFile(w, r, filePath, "")
}

// serveAppStatic serves static files for app UI (React app) from embedded FS
// Falls back to portal.html with SSR when path is root or file not found
func serveAppStatic(w http.ResponseWriter, r *http.Request, appPath string, serv *portal.RelayServer, admin *Admin) {
	// Prevent directory traversal
	if strings.Contains(appPath, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	utils.SetCORSHeaders(w)

	// If path is empty or "/", serve portal.html with SSR
	if appPath == "" || appPath == "/" {
		servePortalHTMLWithSSR(w, r, serv, admin)
		return
	}

	// Try to read from embedded FS
	fullPath := path.Join("dist", "app", appPath)
	data, err := distFS.ReadFile(fullPath)
	if err != nil {
		// File not found - fallback to portal.html with SSR for SPA routing
		log.Debug().Err(err).Str("path", appPath).Msg("app static file not found, falling back to SSR")
		servePortalHTMLWithSSR(w, r, serv, admin)
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
				// brotli-compressed artifact (<hash>.wasm.br) from embed.FS.
				cacheKey := hash + ".wasm"
				if err := cacheWasmFile(cacheKey, fullPath); err != nil {
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
func cacheWasmFile(name, fullPath string) error {
	// Verify name looks like a hex hash (name is <hash>.wasm).
	hashHex := strings.TrimSuffix(name, ".wasm")
	if !utils.IsHexString(hashHex) {
		log.Warn().Str("file", name).Msg("WASM file name is not a valid SHA256 hex string")
	}

	// Load precompressed variant (brotli) from embed.FS (<hash>.wasm.br)
	var brData []byte
	data, err := distFS.ReadFile(fullPath)
	if err != nil {
		log.Warn().Err(err).Str("file", fullPath).Msg("failed to read brotli-compressed WASM")
	} else {
		brData = data
	}

	entry := &wasmCacheEntry{
		brotli: brData,
		hash:   hashHex,
	}

	wasmCacheMu.Lock()
	wasmCache[name] = entry
	wasmCacheMu.Unlock()

	log.Debug().
		Str("file", name).
		Int("brotli", len(entry.brotli)).
		Msg("WASM file cached")

	return nil
}

// serveCompressedWasm serves pre-compressed WASM files from memory cache
func serveCompressedWasm(w http.ResponseWriter, r *http.Request, filePath string) {
	wasmCacheMu.RLock()
	entry, ok := wasmCache[filePath]
	wasmCacheMu.RUnlock()

	if !ok {
		log.Debug().Str("path", filePath).Msg("WASM file not in cache")
		// Fallback: try to serve uncompressed WASM from embedded FS
		fullPath := path.Join("dist", "wasm", filePath)
		data, err := distFS.ReadFile(fullPath)
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
	w.Header().Set("Content-Type", "application/wasm")

	// Check Accept-Encoding header for brotli support
	acceptEncoding := r.Header.Get("Accept-Encoding")

	// Require brotli-compressed WASM
	if !strings.Contains(acceptEncoding, "br") || len(entry.brotli) == 0 {
		log.Warn().
			Str("path", filePath).
			Str("acceptEncoding", acceptEncoding).
			Msg("client does not support brotli or brotli variant missing for WASM")
		http.Error(w, "brotli-compressed WASM required", http.StatusNotAcceptable)
		return
	}

	w.Header().Set("Content-Encoding", "br")
	w.Header().Set("Content-Length", strconv.Itoa(len(entry.brotli)))
	w.WriteHeader(http.StatusOK)
	w.Write(entry.brotli)
	log.Debug().
		Str("path", filePath).
		Int("size", len(entry.brotli)).
		Str("encoding", "brotli").
		Msg("served compressed WASM")
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
func serveStaticFileWithFallback(w http.ResponseWriter, r *http.Request, filePath string, contentType string) {
	utils.SetCORSHeaders(w)

	fullPath := path.Join("dist", "wasm", filePath)
	data, err := distFS.ReadFile(fullPath)
	if err != nil {
		// File not found - fallback to portal.html for SPA routing
		log.Debug().Err(err).Str("path", filePath).Msg("static file not found, serving portal.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		serveStaticFile(w, r, "portal.html", "text/html; charset=utf-8")
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
func serveDynamicManifest(w http.ResponseWriter, _ *http.Request) {
	utils.SetCORSHeaders(w)

	// Find the content-addressed WASM file
	wasmCacheMu.RLock()
	var wasmHash string
	var wasmFile string
	for filename, entry := range wasmCache {
		wasmHash = entry.hash
		wasmFile = filename
		break // Use the first (and should be only) WASM file
	}
	wasmCacheMu.RUnlock()

	// Fallback: scan embedded WASM directory if cache is empty
	if wasmHash == "" {
		entries, err := distFS.ReadDir("dist/wasm")
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

// serveDynamicServiceWorker serves service-worker.js with injected manifest and config
func serveDynamicServiceWorker(w http.ResponseWriter, r *http.Request) {
	utils.SetCORSHeaders(w)

	// Read the service-worker.js template
	fullPath := path.Join("dist", "wasm", "service-worker.js")
	content, err := distFS.ReadFile(fullPath)
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
