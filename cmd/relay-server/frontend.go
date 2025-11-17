package main

import (
	"embed"
	"encoding/json"
	"net/http"
	pathpkg "path"
	"strconv"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
)

//go:embed dist/*
var wasmFS embed.FS

// portalHost is the host for portal frontend.
var portalHost = "localhost"

// portalUIURL is the base URL for portal frontend
var portalUIURL = "http://localhost:4017"

// portalFrontendPattern is the wildcard pattern for portal frontend URLs (e.g., *.localhost:4017)
var portalFrontendPattern = ""

// bootstrapURIs stores the relay bootstrap server URIs
var bootstrapURIs = "ws://localhost:4017/relay"

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
	// Read all files in embedded dist directory
	entries, err := wasmFS.ReadDir("dist")
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Look for content-addressed WASM files: <64-char-hex>.wasm.br
		if strings.HasSuffix(name, ".wasm.br") && len(name) == 72 { // 64 + len(".wasm.br")
			hash := strings.TrimSuffix(name, ".wasm.br")
			if isHexString(hash) && len(hash) == 64 {
				fullPath := pathpkg.Join("dist", name)
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
	// Verify SHA256 hash matches filename (name is <hash>.wasm).
	hashHex := strings.TrimSuffix(name, ".wasm")
	if !isHexString(hashHex) || len(hashHex) != 64 {
		log.Warn().Str("file", name).Msg("WASM file name is not a valid SHA256 hex string")
	}

	// Load precompressed variant (brotli) from embed.FS (<hash>.wasm.br)
	var brData []byte
	data, err := wasmFS.ReadFile(fullPath)
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

// setCORSHeaders sets CORS headers for static file serving
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Accept-Encoding")
}

// createPortalMux creates a new HTTP mux for portal frontend
func createPortalMux() *http.ServeMux {
	// Initialize WASM cache on startup
	if err := initWasmCache(); err != nil {
		log.Error().Err(err).Msg("failed to initialize WASM cache")
	}

	mux := http.NewServeMux()

	// Static file handler for /static/
	mux.HandleFunc("/static/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/static/")
		servePortalStaticFile(w, r, path)
	})

	// Static file handler for /frontend/ (for unified caching)
	mux.HandleFunc("/frontend/", func(w http.ResponseWriter, r *http.Request) {
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

	// Root handler for portal frontend
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/" {
			serveStaticFile(w, r, "portal.html", "text/html; charset=utf-8")
			return
		}

		// Try to serve static files, fallback to portal.html for SPA routing
		servePortalStatic(w, r)
	})

	return mux
}

// servePortalStaticFile serves static files for portal frontend with caching
func servePortalStaticFile(w http.ResponseWriter, r *http.Request, filePath string) {
	// Check if this is a content-addressed WASM file
	if strings.HasSuffix(filePath, ".wasm") && len(filePath) == 69 { // 64 + len(".wasm")
		hash := strings.TrimSuffix(filePath, ".wasm")
		if isHexString(hash) && len(hash) == 64 {
			serveCompressedWasm(w, r, filePath)
			return
		}
	}

	// Regular static file serving
	w.Header().Set("Cache-Control", "public, max-age=3600")
	serveStaticFile(w, r, filePath, "")
}

// serveCompressedWasm serves pre-compressed WASM files from memory cache
func serveCompressedWasm(w http.ResponseWriter, r *http.Request, filePath string) {
	wasmCacheMu.RLock()
	entry, ok := wasmCache[filePath]
	wasmCacheMu.RUnlock()

	if !ok {
		log.Debug().Str("path", filePath).Msg("WASM file not in cache")
		http.NotFound(w, r)
		return
	}

	// Set immutable cache headers for content-addressed files
	setCORSHeaders(w)
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

// serveAdminStatic serves static files for admin UI from embedded FS
func serveAdminStatic(w http.ResponseWriter, r *http.Request, path string) {
	// Prevent directory traversal
	if strings.Contains(path, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	// Try to read from embedded FS
	setCORSHeaders(w)

	fullPath := pathpkg.Join("static", path)
	data, err := assetsFS.ReadFile(fullPath)
	if err != nil {
		log.Debug().Err(err).Str("path", path).Msg("admin static file not found")
		http.NotFound(w, r)
		return
	}

	// Set content type based on extension
	ext := pathpkg.Ext(path)
	contentType := getContentType(ext)
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	w.Write(data)

	log.Debug().
		Str("path", path).
		Int("size", len(data)).
		Msg("served admin static file")
}

// servePortalStatic serves static files for portal frontend with appropriate cache headers
// Falls back to portal.html for SPA routing (404 -> portal.html)
func servePortalStatic(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")

	// Prevent directory traversal
	if strings.Contains(path, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	// Special handling for specific files
	switch path {
	case "manifest.json":
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		w.Header().Set("Content-Type", "application/json")
		serveStaticFileWithFallback(w, r, path, "application/json")
		return

	case "service-worker.js":
		serveDynamicServiceWorker(w, r)
		return

	case "wasm_exec.js":
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("Content-Type", "application/javascript")
		serveStaticFileWithFallback(w, r, path, "application/javascript")
		return

	case "portal.mp4":
		w.Header().Set("Cache-Control", "public, max-age=604800")
		w.Header().Set("Content-Type", "video/mp4")
		serveStaticFileWithFallback(w, r, path, "video/mp4")
		return
	}

	// Default caching for other files
	w.Header().Set("Cache-Control", "public, max-age=3600")
	serveStaticFileWithFallback(w, r, path, "")
}

// serveStaticFile reads and serves a file from the static directory
func serveStaticFile(w http.ResponseWriter, r *http.Request, path string, contentType string) {
	setCORSHeaders(w)

	fullPath := pathpkg.Join("dist", path)
	data, err := wasmFS.ReadFile(fullPath)
	if err != nil {
		log.Debug().Err(err).Str("path", path).Msg("static file not found")
		http.NotFound(w, r)
		return
	}

	// Set content type
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		ext := pathpkg.Ext(path)
		ct := getContentType(ext)
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
	}

	log.Debug().
		Str("path", path).
		Int("size", len(data)).
		Msg("served static file")

	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// serveStaticFileWithFallback reads and serves a file from the static directory
// If the file is not found, it falls back to portal.html for SPA routing
func serveStaticFileWithFallback(w http.ResponseWriter, r *http.Request, path string, contentType string) {
	setCORSHeaders(w)

	fullPath := pathpkg.Join("dist", path)
	data, err := wasmFS.ReadFile(fullPath)
	if err != nil {
		// File not found - fallback to portal.html for SPA routing
		log.Debug().Err(err).Str("path", path).Msg("static file not found, serving portal.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		serveStaticFile(w, r, "portal.html", "text/html; charset=utf-8")
		return
	}

	// Set content type
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		ext := pathpkg.Ext(path)
		ct := getContentType(ext)
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
	}

	log.Debug().
		Str("path", path).
		Int("size", len(data)).
		Msg("served static file")

	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

// getContentType returns the MIME type for a file extension
func getContentType(ext string) string {
	switch ext {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".wasm":
		return "application/wasm"
	case ".css":
		return "text/css"
	case ".mp4":
		return "video/mp4"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	default:
		return ""
	}
}

// isPortalSubdomain checks if the host matches the portal frontend pattern
func isPortalSubdomain(host string) bool {
	// If we have a frontend pattern, use it
	if portalFrontendPattern != "" {
		return matchesWildcardPattern(host, portalFrontendPattern)
	}

	// Fallback to checking if it ends with .{portalHost}
	if portalHost == "" {
		return false
	}
	return strings.HasSuffix(host, "."+portalHost)
}

// matchesWildcardPattern checks if a host matches a wildcard pattern (e.g., *.localhost:4017)
func matchesWildcardPattern(host, pattern string) bool {
	// Handle wildcard pattern (e.g., *.localhost:4017)
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*")
		return strings.HasSuffix(host, suffix)
	}

	// Exact match
	return host == pattern
}

// isHexString checks if a string contains only hexadecimal characters
func isHexString(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// serveDynamicManifest generates and serves manifest.json dynamically
func serveDynamicManifest(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)

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
		entries, err := wasmFS.ReadDir("dist")
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				name := entry.Name()
				// Look for content-addressed WASM files: <64-char-hex>.wasm.br
				if strings.HasSuffix(name, ".wasm.br") && len(name) == 72 {
					hash := strings.TrimSuffix(name, ".wasm.br")
					if isHexString(hash) && len(hash) == 64 {
						wasmHash = hash
						wasmFile = hash + ".wasm"
						break
					}
				}
			}
		}
	}

	// Generate WASM URL
	wasmURL := portalUIURL + "/frontend/" + wasmFile

	// Create manifest structure
	manifest := map[string]string{
		"wasmFile":   wasmFile,
		"wasmUrl":    wasmURL,
		"hash":       wasmHash,
		"bootstraps": bootstrapURIs,
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
		Str("bootstraps", bootstrapURIs).
		Msg("Served dynamic manifest")
}

// serveDynamicServiceWorker serves service-worker.js with injected manifest and config
func serveDynamicServiceWorker(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)

	// Read the service-worker.js template
	fullPath := pathpkg.Join("dist", "service-worker.js")
	content, err := wasmFS.ReadFile(fullPath)
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
