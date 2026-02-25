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

	// Force one-time cleanup of legacy service workers/caches before app boot.
	injectedHTML = strings.Replace(injectedHTML, "</head>", legacyCleanupBootstrapJS+"\n</head>", 1)

	// Set headers
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")

	// Send response
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(injectedHTML))

	log.Debug().Msg("Served portal.html with SSR data")
}

const legacyServiceWorkerCleanupJS = `/* Portal legacy SW cleanup worker */
self.addEventListener("install", (event) => {
  event.waitUntil(self.skipWaiting());
});

self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    try {
      const keys = await caches.keys();
      await Promise.all(keys.map((k) => caches.delete(k)));
    } catch (_) {}

    await self.clients.claim();
    await self.registration.unregister();

    const clients = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    for (const client of clients) {
      client.navigate(client.url);
    }
  })());
});

self.addEventListener("fetch", (event) => {
  event.respondWith(fetch(event.request));
});
`

const legacyCleanupBootstrapJS = `<script>
(function () {
  if (!("serviceWorker" in navigator)) {
    return;
  }

  var marker = "portal-sw-cleanup-v2";
  try {
    if (sessionStorage.getItem(marker) === "1") {
      return;
    }
    sessionStorage.setItem(marker, "1");
  } catch (_) {}

  var unregister = navigator.serviceWorker.getRegistrations().then(function (regs) {
    return Promise.all(
      regs.map(function (reg) {
        return reg.unregister();
      })
    );
  });

  var clearCaches = typeof caches === "undefined"
    ? Promise.resolve()
    : caches.keys().then(function (keys) {
      return Promise.all(
        keys.map(function (k) {
          return caches.delete(k);
        })
      );
    });

  Promise.all([unregister, clearCaches]).finally(function () {
    location.reload();
  });
})();
</script>`

// ServeLegacyServiceWorkerCleanup serves a compatibility service worker
// that unregisters itself and clears caches from legacy webclient deployments.
func (f *Frontend) ServeLegacyServiceWorkerCleanup(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(legacyServiceWorkerCleanupJS))
	}
}

// ServeLegacyFrontendCompat handles removed /frontend/* endpoints from legacy webclient.
func (f *Frontend) ServeLegacyFrontendCompat(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	p := strings.TrimPrefix(r.URL.Path, "/frontend/")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")

	if p == "manifest.json" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"success":false,"message":"legacy webclient removed; refresh required"}`))
		}
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusGone)
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte("legacy webclient assets removed; refresh required"))
	}
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
