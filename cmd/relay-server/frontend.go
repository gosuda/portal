package main

import (
	"embed"
	"encoding/json"
	"errors"
	"html"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/portal/v2/portal"
	"github.com/gosuda/portal/v2/types"
)

type readDirFileFS interface {
	fs.ReadFileFS
	fs.ReadDirFS
}

//go:embed dist/*
var embeddedDistFS embed.FS

type Frontend struct {
	distFS       readDirFileFS
	portalURL    string
	server       *portal.Server
	auth         *adminAuth
	trustProxy   bool
	trustedCIDRs []*net.IPNet

	cachedPortalHTML     []byte
	cachedPortalHTMLOnce sync.Once
}

func NewFrontend(portalURL string, server *portal.Server, adminSecret string, trustedProxyCIDRs []*net.IPNet, trustProxy bool) (*Frontend, error) {
	if server == nil {
		return nil, errors.New("frontend requires portal server")
	}
	runtime := server.PolicyRuntime()
	if runtime == nil {
		return nil, errors.New("frontend requires policy runtime")
	}
	if err := loadAdminState(runtime); err != nil {
		return nil, err
	}

	return &Frontend{
		distFS:       embeddedDistFS,
		portalURL:    strings.TrimSpace(portalURL),
		server:       server,
		auth:         newAdminAuth(adminSecret),
		trustProxy:   trustProxy,
		trustedCIDRs: trustedProxyCIDRs,
	}, nil
}

func (f *Frontend) Handler() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		f.ServeAppStatic(w, r, "")
	})
	mux.HandleFunc(types.PathApp, func(w http.ResponseWriter, r *http.Request) {
		f.ServeAppStatic(w, r, "")
	})
	mux.HandleFunc(types.PathAppPrefix, func(w http.ResponseWriter, r *http.Request) {
		f.ServeAppStatic(w, r, strings.TrimPrefix(strings.TrimSpace(r.URL.Path), types.PathAppPrefix))
	})
	mux.HandleFunc(types.PathAssetsPrefix, func(w http.ResponseWriter, r *http.Request) {
		f.ServeAsset(w, r, strings.TrimPrefix(r.URL.Path, "/"), "")
	})
	for _, assetPath := range frontendRootAssetPaths() {
		mux.HandleFunc(assetPath, func(w http.ResponseWriter, r *http.Request) {
			f.ServeAsset(w, r, strings.TrimPrefix(assetPath, "/"), "")
		})
	}

	mux.HandleFunc(types.PathAdmin, f.serveAdmin)
	mux.HandleFunc(types.PathAdminPrefix, f.serveAdmin)
	mux.HandleFunc(types.PathInstallShell, func(w http.ResponseWriter, r *http.Request) {
		serveInstallScript(w, r, f.portalURL, false)
	})
	mux.HandleFunc(types.PathInstallPowerShell, func(w http.ResponseWriter, r *http.Request) {
		serveInstallScript(w, r, f.portalURL, true)
	})
	mux.HandleFunc(types.PathInstallBinPrefix, serveInstallBinary)

	return mux
}

func (f *Frontend) ServeAsset(w http.ResponseWriter, r *http.Request, assetPath, contentType string) {
	assetPath, ok := cleanFrontendPath(assetPath)
	if !ok {
		http.NotFound(w, r)
		return
	}

	fullPath := path.Join("dist", "app", assetPath)
	data, err := f.distFS.ReadFile(fullPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if contentType == "" {
		contentType = getContentType(path.Ext(assetPath))
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (f *Frontend) ServeAppStatic(w http.ResponseWriter, r *http.Request, appPath string) {
	appPath, ok := cleanFrontendPath(appPath)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if appPath == "" {
		f.servePortalHTMLWithSSR(w)
		return
	}

	fullPath := path.Join("dist", "app", appPath)
	data, err := f.distFS.ReadFile(fullPath)
	if err != nil {
		if path.Ext(appPath) != "" {
			http.NotFound(w, r)
			return
		}
		f.servePortalHTMLWithSSR(w)
		return
	}

	if contentType := getContentType(path.Ext(appPath)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func cleanFrontendPath(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}

	cleaned := strings.TrimPrefix(path.Clean("/"+raw), "/")
	if cleaned == "." || cleaned == "" {
		return "", true
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}

func (f *Frontend) servePortalHTMLWithSSR(w http.ResponseWriter) {
	f.cachedPortalHTMLOnce.Do(func() {
		f.cachedPortalHTML, _ = f.distFS.ReadFile("dist/app/portal.html")
	})

	if len(f.cachedPortalHTML) == 0 {
		http.NotFound(w, nil)
		return
	}

	htmlContent := string(f.cachedPortalHTML)
	htmlContent = f.injectServerData(htmlContent)
	htmlContent = f.injectOGMetadata(htmlContent, "", "", "")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(htmlContent))
}

func (f *Frontend) injectServerData(htmlContent string) string {
	var snapshots []types.Lease
	if f.server != nil {
		snapshots = f.publicLeaseSnapshots()
	}
	jsonData, err := json.Marshal(snapshots)
	if err != nil {
		jsonData = []byte("[]")
	}
	ssrScript := `<script id="__SSR_DATA__" type="application/json">` + string(jsonData) + `</script>`
	return strings.Replace(htmlContent, "</head>", ssrScript+"\n</head>", 1)
}

func (f *Frontend) injectOGMetadata(htmlContent, title, description, imageURL string) string {
	if title == "" {
		title = "Portal Proxy Gateway"
	}
	if description == "" {
		description = "Transform your local services into web-accessible endpoints. Instant access from anywhere."
	}
	if imageURL == "" {
		base := strings.TrimSuffix(f.portalURL, "/")
		if !strings.HasPrefix(base, "http") {
			base = "https://" + base
		}
		imageURL = base + "/portal.jpg"
	}

	replacer := strings.NewReplacer(
		"[%OG_TITLE%]", html.EscapeString(title),
		"[%OG_DESCRIPTION%]", html.EscapeString(description),
		"[%OG_IMAGE_URL%]", html.EscapeString(imageURL),
		"[%RELEASE_VERSION%]", html.EscapeString(types.ReleaseVersion),
	)
	return replacer.Replace(htmlContent)
}

func (f *Frontend) adminLeaseSnapshots() []types.Lease {
	snapshots := f.server.LeaseSnapshots()
	if len(snapshots) == 0 {
		return nil
	}
	now := time.Now()
	filtered := make([]types.Lease, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if now.After(snapshot.ExpiresAt) {
			continue
		}
		filtered = append(filtered, snapshot)
	}
	return filtered
}

func (f *Frontend) publicLeaseSnapshots() []types.Lease {
	snapshots := f.server.LeaseSnapshots()
	if len(snapshots) == 0 {
		return nil
	}

	now := time.Now()
	filtered := make([]types.Lease, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if now.After(snapshot.ExpiresAt) {
			continue
		}
		since := time.Duration(0)
		if !snapshot.LastSeenAt.IsZero() {
			since = max(now.Sub(snapshot.LastSeenAt), 0)
		}
		if snapshot.IsBanned || snapshot.IsDenied || !snapshot.IsApproved || snapshot.Metadata.Hide {
			continue
		}
		if snapshot.Ready == 0 && since >= 3*time.Minute {
			continue
		}

		snapshot.ClientIP = ""
		snapshot.IsApproved = false
		snapshot.IsBanned = false
		snapshot.IsDenied = false
		snapshot.IsIPBanned = false
		filtered = append(filtered, snapshot)
	}
	return filtered
}

func getContentType(ext string) string {
	ext = strings.TrimSpace(ext)
	if ext == "" {
		return ""
	}
	if contentType := mime.TypeByExtension(ext); contentType != "" {
		return contentType
	}

	switch strings.ToLower(ext) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".json", ".webmanifest":
		return "application/json; charset=utf-8"
	default:
		return ""
	}
}

func frontendRootAssetPaths() []string {
	return []string{
		"/favicon.ico",
		"/favicon.svg",
		"/favicon-96x96.png",
		"/apple-touch-icon.png",
		"/web-app-manifest-192x192.png",
		"/web-app-manifest-512x512.png",
		"/portal.jpg",
	}
}
