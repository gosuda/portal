package main

import (
	"encoding/json"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"
	"gosuda.org/portal/portal"
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
}

func NewFrontend() *Frontend {
	return &Frontend{
		distFS: distFS,
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
	setCORSHeaders(w)

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

	// Inject OG metadata (defaults for main app)
	injectedHTML = f.injectOGMetadata(injectedHTML, "", "", "")

	// Set headers
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")

	// Send response
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(injectedHTML))

	log.Debug().Msg("Served portal.html with SSR data")
}

// injectOGMetadata replaces OG placeholders with actual values.
func (f *Frontend) injectOGMetadata(htmlContent, title, description, imageURL string) string {
	if title == "" {
		title = "Portal Proxy Gateway"
	}
	if description == "" {
		description = "Transform your local services into web-accessible endpoints. Instant access from anywhere."
	}
	if imageURL == "" {
		// Use absolute URL if possible
		base := strings.TrimSuffix(flagPortalURL, "/")
		if !strings.HasPrefix(base, "http") {
			base = "https://" + base
		}
		imageURL = base + "/portal.jpg"
	}

	replacer := strings.NewReplacer(
		"[%OG_TITLE%]", html.EscapeString(title),
		"[%OG_DESCRIPTION%]", html.EscapeString(description),
		"[%OG_IMAGE_URL%]", html.EscapeString(imageURL),
	)

	return replacer.Replace(htmlContent)
}

// injectServerData injects server data into HTML for SSR
func (f *Frontend) injectServerData(htmlContent string, serv *portal.RelayServer) string {
	// Get server data from lease manager
	rows := []leaseRow{}
	if f.admin != nil {
		rows = convertLeaseEntriesToRows(serv, f.admin, false)
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

// ServeAppStatic serves static files for app UI (React app) from embedded FS.
// Falls back to portal.html with SSR when path is root or file not found.
func (f *Frontend) ServeAppStatic(w http.ResponseWriter, r *http.Request, appPath string, serv *portal.RelayServer) {
	// Prevent directory traversal
	if strings.Contains(appPath, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	setCORSHeaders(w)

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
	contentType := getContentType(ext)
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
