package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gosuda/portal/v2/cmd/portal-tunnel/installer"
	"github.com/gosuda/portal/v2/portal"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

type readDirFileFS interface {
	fs.ReadFileFS
	fs.ReadDirFS
}

//go:embed dist/*
var embeddedDistFS embed.FS

type Frontend struct {
	distFS            readDirFileFS
	server            *portal.Server
	auth              *adminAuth
	adminSettingsPath string

	cachedPortalHTML     []byte
	cachedPortalHTMLOnce sync.Once
	landingPageEnabled   atomic.Bool
}

func NewFrontend(server *portal.Server, adminSecret string, adminSettingsPath string, defaultLandingPageEnabled bool) (*Frontend, error) {
	if server == nil {
		return nil, errors.New("frontend requires portal server")
	}
	runtime := server.PolicyRuntime()
	if runtime == nil {
		return nil, errors.New("frontend requires policy runtime")
	}
	state, err := loadAdminState(adminSettingsPath, runtime)
	if err != nil {
		return nil, err
	}

	frontend := &Frontend{
		distFS:            embeddedDistFS,
		server:            server,
		auth:              newAdminAuth(adminSecret),
		adminSettingsPath: strings.TrimSpace(adminSettingsPath),
	}
	landingPageEnabled := defaultLandingPageEnabled
	if state.LandingPageEnabled != nil {
		landingPageEnabled = *state.LandingPageEnabled
	}
	frontend.setLandingPageEnabled(landingPageEnabled)
	return frontend, nil
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
		f.ServeAppStatic(w, r, strings.TrimPrefix(r.URL.Path, types.PathAppPrefix))
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
	mux.HandleFunc(types.PathTunnelStatus, f.serveTunnelStatus)
	mux.HandleFunc(types.PathInstallShell, func(w http.ResponseWriter, r *http.Request) {
		serveInstallScript(w, r, f.server.PortalURL(), false)
	})
	mux.HandleFunc(types.PathInstallPowerShell, func(w http.ResponseWriter, r *http.Request) {
		serveInstallScript(w, r, f.server.PortalURL(), true)
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
	htmlContent = f.injectOGMetadata(htmlContent, "", "")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(htmlContent))
}

func (f *Frontend) injectServerData(htmlContent string) string {
	var snapshots []types.Lease
	if f.server != nil {
		snapshots = f.server.LeaseSnapshots()
	}
	jsonData, err := json.Marshal(snapshots)
	if err != nil {
		jsonData = []byte("[]")
	}
	ssrScript := `<script id="__SSR_DATA__" type="application/json">` + string(jsonData) + `</script>`
	return strings.Replace(htmlContent, "</head>", ssrScript+"\n</head>", 1)
}

func (f *Frontend) serveTunnelStatus(w http.ResponseWriter, r *http.Request) {
	if !utils.RequireMethod(w, r, http.MethodGet) {
		return
	}

	hostname := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("hostname")))
	if hostname == "" {
		utils.InvalidRequestMessage("hostname is required").Write(w)
		return
	}

	resp := types.TunnelStatusResponse{
		Hostname: hostname,
	}
	if snapshot, ok := f.server.LeaseSnapshotByHostname(hostname); ok {
		resp.Hostname = snapshot.Hostname
		resp.Registered = true
		resp.ServiceAlive = snapshot.Ready > 0
	}
	utils.WriteAPIData(w, http.StatusOK, resp)
}

func (f *Frontend) injectOGMetadata(htmlContent, title, description string) string {
	if title == "" {
		title = "Portal Proxy Gateway"
	}
	if description == "" {
		description = "Transform your local services into web-accessible endpoints. Instant access from anywhere."
	}

	replacer := strings.NewReplacer(
		"[%OG_TITLE%]", html.EscapeString(title),
		"[%OG_DESCRIPTION%]", html.EscapeString(description),
		"[%LANDING_PAGE_ENABLED%]", html.EscapeString(strconv.FormatBool(f.isLandingPageEnabled())),
		"[%RELEASE_VERSION%]", html.EscapeString(types.ReleaseVersion),
	)
	return replacer.Replace(htmlContent)
}

func (f *Frontend) isLandingPageEnabled() bool {
	if f == nil {
		return false
	}
	return f.landingPageEnabled.Load()
}

func (f *Frontend) setLandingPageEnabled(enabled bool) {
	if f == nil {
		return
	}
	f.landingPageEnabled.Store(enabled)
}

func getContentType(ext string) string {
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	if ext == ".webmanifest" {
		return "application/json; charset=utf-8"
	}
	return ""
}

func frontendRootAssetPaths() []string {
	return []string{
		"/favicon.ico",
		"/favicon.svg",
		"/favicon-96x96.png",
		"/apple-touch-icon.png",
		"/web-app-manifest-192x192.png",
		"/web-app-manifest-512x512.png",
	}
}

func serveInstallBinary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slug := strings.Trim(strings.TrimPrefix(r.URL.Path, types.PathInstallBinPrefix), "/")
	checksumRequest := strings.HasSuffix(slug, ".sha256")
	if checksumRequest {
		slug = strings.TrimSuffix(slug, ".sha256")
	}

	filename, ok := installer.AssetFilename(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := embeddedDistFS.ReadFile("dist/tunnel/" + filename)
	if err != nil {
		redirectURL, ok := installer.OfficialAssetURL(slug, checksumRequest)
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
		return
	}
	sum := sha256.Sum256(data)
	checksumHex := hex.EncodeToString(sum[:])

	if checksumRequest {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Method == http.MethodGet {
			_, _ = fmt.Fprintf(w, "%s  %s\n", checksumHex, filename)
		}
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("X-Checksum-Sha256", checksumHex)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func serveInstallScript(w http.ResponseWriter, r *http.Request, portalURL string, isWindows bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	script, filename, contentType, err := installer.RelayScript(portalURL, isWindows)
	if err != nil {
		http.Error(w, "failed to render install script", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filename))
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(script))
	}
}
