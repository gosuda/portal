package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/rs/zerolog/log"
)

// staticDir is the directory where static files are located
var staticDir = "./dist"

// portalDomain is the domain for portal frontend (e.g., "portal.gosuda.org")
var portalDomain = "portal.gosuda.org"

// wasmCache stores pre-compressed WASM files in memory
type wasmCacheEntry struct {
	original []byte
	brotli   []byte
	gzip     []byte
	hash     string
}

var (
	wasmCache   = make(map[string]*wasmCacheEntry)
	wasmCacheMu sync.RWMutex
)

// initWasmCache pre-compresses and caches all WASM files on startup
func initWasmCache() error {
	// Read all files in staticDir
	entries, err := os.ReadDir(staticDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Look for content-addressed WASM files: <64-char-hex>.wasm
		if strings.HasSuffix(name, ".wasm") && len(name) == 69 { // 64 + len(".wasm")
			hash := strings.TrimSuffix(name, ".wasm")
			if isHexString(hash) && len(hash) == 64 {
				fullPath := filepath.Join(staticDir, name)
				if err := cacheWasmFile(name, fullPath); err != nil {
					log.Warn().Err(err).Str("file", name).Msg("failed to cache WASM file")
				} else {
					log.Info().Str("file", name).Msg("cached and compressed WASM file")
				}
			}
		}
	}

	return nil
}

// cacheWasmFile reads, compresses, and caches a WASM file
func cacheWasmFile(name, fullPath string) error {
	// Read original file
	original, err := os.ReadFile(fullPath)
	if err != nil {
		return err
	}

	// Verify SHA256 hash matches filename
	hash := sha256.Sum256(original)
	expectedHash := strings.TrimSuffix(name, ".wasm")
	actualHash := hex.EncodeToString(hash[:])
	if expectedHash != actualHash {
		log.Warn().
			Str("file", name).
			Str("expected", expectedHash).
			Str("actual", actualHash).
			Msg("WASM file hash mismatch")
	}

	// Compress with brotli (level 11 for maximum compression)
	var brBuf bytes.Buffer
	brWriter := brotli.NewWriterLevel(&brBuf, 11)
	if _, err := brWriter.Write(original); err != nil {
		return err
	}
	if err := brWriter.Close(); err != nil {
		return err
	}

	// Compress with gzip as fallback
	var gzBuf bytes.Buffer
	gzWriter := gzip.NewWriter(&gzBuf)
	if _, err := gzWriter.Write(original); err != nil {
		return err
	}
	if err := gzWriter.Close(); err != nil {
		return err
	}

	entry := &wasmCacheEntry{
		original: original,
		brotli:   brBuf.Bytes(),
		gzip:     gzBuf.Bytes(),
		hash:     actualHash,
	}

	wasmCacheMu.Lock()
	wasmCache[name] = entry
	wasmCacheMu.Unlock()

	log.Debug().
		Str("file", name).
		Int("original", len(original)).
		Int("brotli", len(entry.brotli)).
		Int("gzip", len(entry.gzip)).
		Float64("br_ratio", float64(len(entry.brotli))/float64(len(original))*100).
		Float64("gz_ratio", float64(len(entry.gzip))/float64(len(original))*100).
		Msg("WASM file cached")

	return nil
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
		path := strings.TrimPrefix(r.URL.Path, "/static/")
		servePortalStaticFile(w, r, path)
	})

	// Root handler for portal frontend
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
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
func servePortalStaticFile(w http.ResponseWriter, r *http.Request, path string) {
	// Check if this is a content-addressed WASM file
	if strings.HasSuffix(path, ".wasm") && len(path) == 69 { // 64 + len(".wasm")
		hash := strings.TrimSuffix(path, ".wasm")
		if isHexString(hash) && len(hash) == 64 {
			serveCompressedWasm(w, r, path)
			return
		}
	}

	// Regular static file serving
	w.Header().Set("Cache-Control", "public, max-age=3600")
	serveStaticFile(w, r, path, "")
}

// serveCompressedWasm serves pre-compressed WASM files from memory cache
func serveCompressedWasm(w http.ResponseWriter, r *http.Request, path string) {
	wasmCacheMu.RLock()
	entry, ok := wasmCache[path]
	wasmCacheMu.RUnlock()

	if !ok {
		log.Debug().Str("path", path).Msg("WASM file not in cache")
		http.NotFound(w, r)
		return
	}

	// Set immutable cache headers for content-addressed files
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Content-Type", "application/wasm")

	// Check Accept-Encoding header for compression support
	acceptEncoding := r.Header.Get("Accept-Encoding")

	// Prefer brotli if supported
	if strings.Contains(acceptEncoding, "br") {
		w.Header().Set("Content-Encoding", "br")
		w.Header().Set("Content-Length", strconv.Itoa(len(entry.brotli)))
		w.WriteHeader(http.StatusOK)
		w.Write(entry.brotli)
		log.Debug().
			Str("path", path).
			Int("size", len(entry.brotli)).
			Str("encoding", "brotli").
			Msg("served compressed WASM")
		return
	}

	// Fall back to gzip if supported
	if strings.Contains(acceptEncoding, "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", strconv.Itoa(len(entry.gzip)))
		w.WriteHeader(http.StatusOK)
		w.Write(entry.gzip)
		log.Debug().
			Str("path", path).
			Int("size", len(entry.gzip)).
			Str("encoding", "gzip").
			Msg("served compressed WASM")
		return
	}

	// No compression support - serve original
	w.Header().Set("Content-Length", strconv.Itoa(len(entry.original)))
	w.WriteHeader(http.StatusOK)
	w.Write(entry.original)
	log.Debug().
		Str("path", path).
		Int("size", len(entry.original)).
		Str("encoding", "none").
		Msg("served uncompressed WASM")
}

// serveAdminStatic serves static files for admin UI from embedded FS
func serveAdminStatic(w http.ResponseWriter, r *http.Request, path string) {
	// Prevent directory traversal
	if strings.Contains(path, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	// Try to read from embedded FS
	fullPath := filepath.Join("static", path)
	data, err := assetsFS.ReadFile(fullPath)
	if err != nil {
		log.Debug().Err(err).Str("path", path).Msg("admin static file not found")
		http.NotFound(w, r)
		return
	}

	// Set content type based on extension
	ext := filepath.Ext(path)
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
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		w.Header().Set("Content-Type", "application/javascript")
		serveStaticFileWithFallback(w, r, path, "application/javascript")
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
	fullPath := filepath.Join(staticDir, path)

	file, err := os.Open(fullPath)
	if err != nil {
		log.Debug().Err(err).Str("path", path).Msg("static file not found")
		http.NotFound(w, r)
		return
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		log.Error().Err(err).Str("path", path).Msg("failed to stat file")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Set content type
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		ext := filepath.Ext(path)
		ct := getContentType(ext)
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
	}

	w.WriteHeader(http.StatusOK)
	io.Copy(w, file)

	log.Debug().
		Str("path", path).
		Int64("size", fileInfo.Size()).
		Msg("served static file")
}

// serveStaticFileWithFallback reads and serves a file from the static directory
// If the file is not found, it falls back to portal.html for SPA routing
func serveStaticFileWithFallback(w http.ResponseWriter, r *http.Request, path string, contentType string) {
	fullPath := filepath.Join(staticDir, path)

	file, err := os.Open(fullPath)
	if err != nil {
		// File not found - fallback to portal.html for SPA routing
		log.Debug().Err(err).Str("path", path).Msg("static file not found, serving portal.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		serveStaticFile(w, r, "portal.html", "text/html; charset=utf-8")
		return
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		log.Error().Err(err).Str("path", path).Msg("failed to stat file")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Set content type
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		ext := filepath.Ext(path)
		ct := getContentType(ext)
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
	}

	w.WriteHeader(http.StatusOK)
	io.Copy(w, file)

	log.Debug().
		Str("path", path).
		Int64("size", fileInfo.Size()).
		Msg("served static file")
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

// isPortalSubdomain checks if the host is a portal subdomain (*.{portalDomain})
func isPortalSubdomain(host string) bool {
	// Check if it ends with .{portalDomain} or is exactly {portalDomain}
	return strings.HasSuffix(host, "."+portalDomain)
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
