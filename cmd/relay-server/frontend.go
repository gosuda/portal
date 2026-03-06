package main

import (
	"embed"
	"encoding/json"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"

	"gosuda.org/portal/portal"
)

type readDirFileFS interface {
	fs.ReadFileFS
	fs.ReadDirFS
}

//go:embed dist/*
var embeddedDistFS embed.FS

type Frontend struct {
	distFS    readDirFileFS
	portalURL string
	server    *portal.Server

	cachedPortalHTML     []byte
	cachedPortalHTMLOnce sync.Once
}

func NewFrontend(portalURL string) *Frontend {
	return &Frontend{
		distFS:    embeddedDistFS,
		portalURL: strings.TrimSpace(portalURL),
	}
}

func (f *Frontend) Bind(server *portal.Server) {
	f.server = server
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
	rows := convertLeaseEntriesToRows(f.server, false, f.portalURL)
	jsonData, err := json.Marshal(rows)
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
	)
	return replacer.Replace(htmlContent)
}
