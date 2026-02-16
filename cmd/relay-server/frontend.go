package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"path"
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
	utils.SetCORSHeaders(w)

	// Initialize cache on first use.
	f.cachedPortalHTMLOnce.Do(func() {
		if err := f.initPortalHTMLCache(); err != nil {
			log.Error().Err(err).Msg("Failed to cache portal.html")
		}
	})

	if f.cachedPortalHTML == nil {
		http.NotFound(w, r)
		return
	}

	// Inject SSR data into cached template.
	injectedHTML := f.injectServerData(string(f.cachedPortalHTML), serv)

	// Inject OG metadata (defaults for main app).
	injectedHTML = f.injectOGMetadata(injectedHTML, "", "", "")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(injectedHTML))

	log.Debug().Msg("Served portal.html with SSR data")
}

// ServePortalHTMLWithSSR serves portal.html for subdomain requests with SSR OG metadata.
func (f *Frontend) ServePortalHTMLWithSSR(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer) {
	utils.SetCORSHeaders(w)

	data, err := f.distFS.ReadFile("dist/app/portal.html")
	if err != nil {
		log.Error().Err(err).Msg("Failed to read dist/app/portal.html")
		http.NotFound(w, r)
		return
	}

	htmlContent := string(data)
	title := ""
	description := ""
	imageURL := ""

	// Extract lease name from host.
	leaseName := ""
	h := strings.ToLower(utils.StripPort(utils.StripScheme(r.Host)))
	p := strings.ToLower(utils.StripPort(utils.StripScheme(flagPortalAppURL)))
	if strings.HasPrefix(p, "*.") {
		suffix := p[1:] // .example.com
		if strings.HasSuffix(h, suffix) {
			leaseName = h[:len(h)-len(suffix)]
		}
	}

	if leaseName != "" {
		if lease, ok := serv.GetLeaseByName(leaseName); ok {
			title = lease.Lease.Name
			if lease.ParsedMetadata != nil {
				description = lease.ParsedMetadata.Description
				imageURL = lease.ParsedMetadata.Thumbnail
			}
		}
	}

	htmlContent = f.injectOGMetadata(htmlContent, title, description, imageURL)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(htmlContent))

	log.Debug().Str("lease", leaseName).Msg("Served portal.html with subdomain SSR OG metadata")
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

// injectServerData injects server data into HTML for SSR.
func (f *Frontend) injectServerData(htmlContent string, serv *portal.RelayServer) string {
	rows := []leaseRow{}
	if f.admin != nil {
		rows = convertLeaseEntriesToRows(serv, f.admin)
	}

	jsonData, err := json.Marshal(rows)
	if err != nil {
		log.Error().Err(err).Msg("Failed to marshal server data for SSR")
		jsonData = []byte("[]")
	}

	ssrScript := `<script id="__SSR_DATA__" type="application/json">` + string(jsonData) + `</script>`
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

		since := max(now.Sub(leaseEntry.LastSeen), 0)
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
	w.Header().Set("Cache-Control", "public, max-age=3600")
	f.serveStaticFile(w, r, filePath, "")
}

// ServeAppStatic serves static files for app UI (React app) from embedded FS.
// Falls back to portal.html with SSR when path is root or file not found.
func (f *Frontend) ServeAppStatic(w http.ResponseWriter, r *http.Request, appPath string, serv *portal.RelayServer) {
	if strings.Contains(appPath, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	utils.SetCORSHeaders(w)

	if appPath == "" || appPath == "/" {
		f.servePortalHTMLWithSSR(w, r, serv)
		return
	}

	fullPath := path.Join("dist", "app", appPath)
	data, err := f.distFS.ReadFile(fullPath)
	if err != nil {
		log.Debug().Err(err).Str("path", appPath).Msg("app static file not found, falling back to SSR")
		f.servePortalHTMLWithSSR(w, r, serv)
		return
	}

	ext := path.Ext(appPath)
	contentType := utils.GetContentType(ext)
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)

	log.Debug().
		Str("path", appPath).
		Int("size", len(data)).
		Msg("served app static file")
}

// ServePortalStatic serves static files for portal frontend with appropriate cache headers.
// Falls back to portal.html for SPA routing (404 -> portal.html).
func (f *Frontend) ServePortalStatic(w http.ResponseWriter, r *http.Request) {
	staticPath := strings.TrimPrefix(r.URL.Path, "/")

	if strings.Contains(staticPath, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	switch staticPath {
	case "portal.mp4":
		w.Header().Set("Cache-Control", "public, max-age=604800")
		w.Header().Set("Content-Type", "video/mp4")
		f.serveStaticFile(w, r, staticPath, "video/mp4")
		return

	case "portal.jpg":
		w.Header().Set("Cache-Control", "public, max-age=604800")
		w.Header().Set("Content-Type", "image/jpeg")
		f.serveStaticFile(w, r, staticPath, "image/jpeg")
		return
	}

	w.Header().Set("Cache-Control", "public, max-age=3600")
	f.serveStaticFileWithFallback(w, r, staticPath, "")
}

// serveStaticFile reads and serves a file from the app static directory.
func (f *Frontend) serveStaticFile(w http.ResponseWriter, r *http.Request, filePath string, contentType string) {
	utils.SetCORSHeaders(w)

	fullPath := path.Join("dist", "app", filePath)
	data, err := f.distFS.ReadFile(fullPath)
	if err != nil {
		log.Debug().Err(err).Str("path", filePath).Msg("static file not found")
		http.NotFound(w, r)
		return
	}

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
	_, _ = w.Write(data)
}

// serveStaticFileWithFallback reads and serves a file from the app static directory.
// If the file is not found, it falls back to portal.html for SPA routing.
func (f *Frontend) serveStaticFileWithFallback(w http.ResponseWriter, r *http.Request, filePath string, contentType string) {
	utils.SetCORSHeaders(w)

	fullPath := path.Join("dist", "app", filePath)
	data, err := f.distFS.ReadFile(fullPath)
	if err != nil {
		log.Debug().Err(err).Str("path", filePath).Msg("static file not found, serving portal.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		f.serveStaticFile(w, r, "portal.html", "text/html; charset=utf-8")
		return
	}

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
	_, _ = w.Write(data)
}
