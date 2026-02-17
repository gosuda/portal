package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
)

func TestSetAdmin(t *testing.T) {
	t.Parallel()

	f := NewFrontend("https://portal.example.com", "https://*.portal.example.com")
	admin := NewAdmin(0, f, manager.NewAuthManager("admin-secret"), f.portalURL, f.portalAppURL)

	f.SetAdmin(admin)

	if f.admin != admin {
		t.Fatalf("frontend admin pointer not assigned")
	}
}

func TestConvertLeaseEntriesToRows(t *testing.T) {
	t.Parallel()

	f := NewFrontend("https://portal.example.com", "https://*.portal.example.com")
	admin := NewAdmin(0, f, manager.NewAuthManager("admin-secret"), f.portalURL, f.portalAppURL)
	f.SetAdmin(admin)
	admin.GetApproveManager().SetApprovalMode(manager.ApprovalModeManual)

	serv := newTestRelayServer(t)
	leaseManager := serv.GetLeaseManager()

	updateLease := func(id, name, metadata string, alpn []string, connectionID int64) {
		t.Helper()

		ok := leaseManager.UpdateLease(&rdverb.Lease{
			Identity: &rdsec.Identity{Id: id},
			Expires:  time.Now().Add(time.Hour).Unix(),
			Name:     name,
			Alpn:     alpn,
			Metadata: metadata,
		}, connectionID)
		if !ok {
			t.Fatalf("update lease %q failed", id)
		}
	}

	updateLease("lease-visible", "", `{"description":"Visible lease"}`, []string{"tcp"}, 101)
	admin.GetApproveManager().ApproveLease("lease-visible")
	admin.GetBPSManager().SetBPSLimit("lease-visible", 2048)
	visibleLease, ok := leaseManager.GetLeaseByID("lease-visible")
	if !ok {
		t.Fatal("visible lease not found")
	}
	visibleLease.LastSeen = time.Now()

	updateLease("lease-hidden", "hidden", `{"hide":true}`, []string{"http/1.1"}, 102)
	admin.GetApproveManager().ApproveLease("lease-hidden")

	updateLease("lease-stale", "stale", `{"description":"stale"}`, nil, 103)
	admin.GetApproveManager().ApproveLease("lease-stale")
	staleLease, ok := leaseManager.GetLeaseByID("lease-stale")
	if !ok {
		t.Fatal("stale lease not found")
	}
	staleLease.LastSeen = time.Now().Add(-4 * time.Minute)

	updateLease("lease-unapproved", "pending", `{"description":"pending"}`, nil, 104)

	updateLease("lease-banned", "banned", `{"description":"banned"}`, nil, 105)
	admin.GetApproveManager().ApproveLease("lease-banned")
	leaseManager.BanLease("lease-banned")

	rows := convertLeaseEntriesToRows(serv, admin, f.portalURL, f.portalAppURL)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}

	row := rows[0]
	expectedFallbackName := "(" + "unnamed" + ")"
	if row.Peer != "lease-visible" {
		t.Fatalf("row.Peer = %q, want %q", row.Peer, "lease-visible")
	}
	if row.Name != expectedFallbackName {
		t.Fatalf("row.Name = %q, want %q", row.Name, expectedFallbackName)
	}
	if row.Kind != "tcp" {
		t.Fatalf("row.Kind = %q, want %q", row.Kind, "tcp")
	}
	if row.BPS != 2048 {
		t.Fatalf("row.BPS = %d, want %d", row.BPS, 2048)
	}
	if row.Link != "//.portal.example.com/" {
		t.Fatalf("row.Link = %q, want %q", row.Link, "//.portal.example.com/")
	}
	if row.StaleRed {
		t.Fatal("row.StaleRed = true, want false for recent lease")
	}
}

func TestInjectOGMetadata(t *testing.T) {
	t.Parallel()

	f := NewFrontend("https://portal.example.com", "https://*.portal.example.com")

	tests := []struct {
		name        string
		title       string
		description string
		imageURL    string
		html        string
		want        []string // strings that should be present in the output
	}{
		{
			name:        "Basic injection",
			title:       "Hello World",
			description: "This is a test description",
			imageURL:    "https://example.com/image.png",
			html:        "<title>[%OG_TITLE%]</title><meta name=\"description\" content=\"[%OG_DESCRIPTION%]\"><meta property=\"og:image\" content=\"[%OG_IMAGE_URL%]\">",
			want: []string{
				"<title>Hello World</title>",
				"content=\"This is a test description\"",
				"content=\"https://example.com/image.png\"",
			},
		},
		{
			name:        "HTML Escaping",
			title:       "<script>alert('xss')</script>",
			description: "Double \"quotes\" and <tags>",
			imageURL:    "https://example.com/img?q=1&b=2",
			html:        "[%OG_TITLE%] | [%OG_DESCRIPTION%] | [%OG_IMAGE_URL%]",
			want: []string{
				"&lt;script&gt;alert(&#39;xss&#39;)&lt;/script&gt;",
				"Double &#34;quotes&#34; and &lt;tags&gt;",
				"https://example.com/img?q=1&amp;b=2",
			},
		},
		{
			name:        "Empty values (Defaults)",
			title:       "",
			description: "",
			imageURL:    "",
			html:        "[%OG_TITLE%] | [%OG_DESCRIPTION%] | [%OG_IMAGE_URL%]",
			want: []string{
				"Portal Proxy Gateway",
				"Transform your local services into web-accessible endpoints",
				"https://portal.example.com/portal.jpg",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := f.injectOGMetadata(tt.html, tt.title, tt.description, tt.imageURL)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("injectOGMetadata() = %v, want to contain %v", got, w)
				}
			}
		})
	}
}

func TestServeAsset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		dist            fstest.MapFS
		assetPath       string
		contentType     string
		wantStatus      int
		wantBody        string
		wantContentType string
	}{
		{
			name: "success",
			dist: fstest.MapFS{
				"dist/app/assets/app.js": {Data: []byte("console.log('asset');")},
			},
			assetPath:       "assets/app.js",
			contentType:     "application/javascript",
			wantStatus:      http.StatusOK,
			wantBody:        "console.log('asset');",
			wantContentType: "application/javascript",
		},
		{
			name:        "missing file returns not found",
			dist:        fstest.MapFS{},
			assetPath:   "assets/missing.js",
			contentType: "application/javascript",
			wantStatus:  http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newTestFrontendWithDistFS(tt.dist)
			mux := http.NewServeMux()
			f.ServeAsset(mux, "/asset.js", tt.assetPath, tt.contentType)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/asset.js", http.NoBody)
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tt.wantBody)
			}
			if tt.wantContentType != "" && rec.Header().Get("Content-Type") != tt.wantContentType {
				t.Fatalf("content-type = %q, want %q", rec.Header().Get("Content-Type"), tt.wantContentType)
			}
		})
	}
}

func TestServeAppStatic(t *testing.T) {
	t.Parallel()

	const portalHTML = `
<!doctype html>
<html>
  <head>
    <title>[%OG_TITLE%]</title>
    <meta name="description" content="[%OG_DESCRIPTION%]">
    <meta property="og:image" content="[%OG_IMAGE_URL%]">
  </head>
  <body>portal app</body>
</html>`

	t.Run("invalid path", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/app", http.NoBody)

		f.ServeAppStatic(rec, req, "../secret", nil)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if !strings.Contains(rec.Body.String(), "Invalid path") {
			t.Fatalf("body = %q, want to contain %q", rec.Body.String(), "Invalid path")
		}
	})

	t.Run("root path serves SSR html", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/portal.html": {Data: []byte(portalHTML)},
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/app/", http.NoBody)

		f.ServeAppStatic(rec, req, "/", nil)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("content-type = %q, want %q", rec.Header().Get("Content-Type"), "text/html; charset=utf-8")
		}
		if rec.Header().Get("Cache-Control") != "no-cache, must-revalidate" {
			t.Fatalf("cache-control = %q, want %q", rec.Header().Get("Cache-Control"), "no-cache, must-revalidate")
		}

		body := rec.Body.String()
		if !strings.Contains(body, "Portal Proxy Gateway") {
			t.Fatalf("body = %q, want default OG title", body)
		}
		if !strings.Contains(body, "__SSR_DATA__") {
			t.Fatalf("body = %q, want SSR data script marker", body)
		}
	})

	t.Run("existing static file", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/assets/app.js": {Data: []byte("console.log('app');")},
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/app/assets/app.js", http.NoBody)

		f.ServeAppStatic(rec, req, "assets/app.js", nil)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Header().Get("Content-Type") != "application/javascript" {
			t.Fatalf("content-type = %q, want %q", rec.Header().Get("Content-Type"), "application/javascript")
		}
		if rec.Header().Get("Cache-Control") != "public, max-age=3600" {
			t.Fatalf("cache-control = %q, want %q", rec.Header().Get("Cache-Control"), "public, max-age=3600")
		}
		if rec.Body.String() != "console.log('app');" {
			t.Fatalf("body = %q, want %q", rec.Body.String(), "console.log('app');")
		}
	})

	t.Run("missing file falls back to SSR", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/portal.html": {Data: []byte(portalHTML)},
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/app/missing.js", http.NoBody)

		f.ServeAppStatic(rec, req, "missing.js", nil)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("content-type = %q, want %q", rec.Header().Get("Content-Type"), "text/html; charset=utf-8")
		}
		if rec.Header().Get("Cache-Control") != "no-cache, must-revalidate" {
			t.Fatalf("cache-control = %q, want %q", rec.Header().Get("Cache-Control"), "no-cache, must-revalidate")
		}
		if !strings.Contains(rec.Body.String(), "Portal Proxy Gateway") {
			t.Fatalf("body = %q, want default OG metadata", rec.Body.String())
		}
	})
}

func TestServePortalStatic(t *testing.T) {
	t.Parallel()

	t.Run("invalid path", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/../secret", http.NoBody)

		f.ServePortalStatic(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
		if !strings.Contains(rec.Body.String(), "Invalid path") {
			t.Fatalf("body = %q, want to contain %q", rec.Body.String(), "Invalid path")
		}
	})

	t.Run("portal.jpg special headers", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/portal.jpg": {Data: []byte("jpg-bytes")},
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/portal.jpg", http.NoBody)

		f.ServePortalStatic(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Header().Get("Cache-Control") != "public, max-age=604800" {
			t.Fatalf("cache-control = %q, want %q", rec.Header().Get("Cache-Control"), "public, max-age=604800")
		}
		if rec.Header().Get("Content-Type") != "image/jpeg" {
			t.Fatalf("content-type = %q, want %q", rec.Header().Get("Content-Type"), "image/jpeg")
		}
		if rec.Body.String() != "jpg-bytes" {
			t.Fatalf("body = %q, want %q", rec.Body.String(), "jpg-bytes")
		}
	})

	t.Run("portal.mp4 special headers", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/portal.mp4": {Data: []byte("mp4-bytes")},
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/portal.mp4", http.NoBody)

		f.ServePortalStatic(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Header().Get("Cache-Control") != "public, max-age=604800" {
			t.Fatalf("cache-control = %q, want %q", rec.Header().Get("Cache-Control"), "public, max-age=604800")
		}
		if rec.Header().Get("Content-Type") != "video/mp4" {
			t.Fatalf("content-type = %q, want %q", rec.Header().Get("Content-Type"), "video/mp4")
		}
		if rec.Body.String() != "mp4-bytes" {
			t.Fatalf("body = %q, want %q", rec.Body.String(), "mp4-bytes")
		}
	})

	t.Run("generic missing path falls back to portal.html", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/portal.html": {Data: []byte("<html>portal fallback</html>")},
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/some/deep/route", http.NoBody)

		f.ServePortalStatic(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Header().Get("Cache-Control") != "public, max-age=3600" {
			t.Fatalf("cache-control = %q, want %q", rec.Header().Get("Cache-Control"), "public, max-age=3600")
		}
		if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("content-type = %q, want %q", rec.Header().Get("Content-Type"), "text/html; charset=utf-8")
		}
		if rec.Body.String() != "<html>portal fallback</html>" {
			t.Fatalf("body = %q, want %q", rec.Body.String(), "<html>portal fallback</html>")
		}
	})
}

func TestServePortalStaticFile(t *testing.T) {
	t.Parallel()

	f := newTestFrontendWithDistFS(fstest.MapFS{
		"dist/app/assets/site.css": {Data: []byte("body { color: #111; }")},
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/assets/site.css", http.NoBody)

	f.ServePortalStaticFile(rec, req, "assets/site.css")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=3600" {
		t.Fatalf("cache-control = %q, want %q", rec.Header().Get("Cache-Control"), "public, max-age=3600")
	}
	if rec.Header().Get("Content-Type") != "text/css" {
		t.Fatalf("content-type = %q, want %q", rec.Header().Get("Content-Type"), "text/css")
	}
	if rec.Body.String() != "body { color: #111; }" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "body { color: #111; }")
	}
}

func TestServeStaticFileWithFallback(t *testing.T) {
	t.Parallel()

	t.Run("existing file infers content type", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/assets/data.json": {Data: []byte(`{"ok":true}`)},
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/assets/data.json", http.NoBody)
		f.serveStaticFileWithFallback(rec, req, "assets/data.json", "")

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("content-type = %q, want %q", rec.Header().Get("Content-Type"), "application/json")
		}
		if rec.Body.String() != `{"ok":true}` {
			t.Fatalf("body = %q, want %q", rec.Body.String(), `{"ok":true}`)
		}
	})

	t.Run("existing file respects explicit content type", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/assets/blob.bin": {Data: []byte("blob")},
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/assets/blob.bin", http.NoBody)
		f.serveStaticFileWithFallback(rec, req, "assets/blob.bin", "application/octet-stream")

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Header().Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("content-type = %q, want %q", rec.Header().Get("Content-Type"), "application/octet-stream")
		}
		if rec.Body.String() != "blob" {
			t.Fatalf("body = %q, want %q", rec.Body.String(), "blob")
		}
	})

	t.Run("missing file falls back to portal html", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/portal.html": {Data: []byte("<html>fallback</html>")},
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/missing", http.NoBody)
		f.serveStaticFileWithFallback(rec, req, "does-not-exist", "application/octet-stream")

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("content-type = %q, want %q", rec.Header().Get("Content-Type"), "text/html; charset=utf-8")
		}
		if rec.Body.String() != "<html>fallback</html>" {
			t.Fatalf("body = %q, want %q", rec.Body.String(), "<html>fallback</html>")
		}
	})
}

func TestServePortalHTMLWithSSR(t *testing.T) {
	t.Parallel()

	f := newTestFrontendWithDistFS(fstest.MapFS{
		"dist/app/portal.html": {
			Data: []byte(`<html><head><title>[%OG_TITLE%]</title><meta name="description" content="[%OG_DESCRIPTION%]"><meta property="og:image" content="[%OG_IMAGE_URL%]"></head><body></body></html>`),
		},
	})

	serv := newTestRelayServer(t)
	ok := serv.GetLeaseManager().UpdateLease(&rdverb.Lease{
		Identity: &rdsec.Identity{Id: "lease-1"},
		Expires:  time.Now().Add(1 * time.Hour).Unix(),
		Name:     "demo",
		Metadata: `{"description":"Demo service","thumbnail":"https://cdn.example.com/demo.jpg"}`,
	}, 1)
	if !ok {
		t.Fatal("expected lease update to succeed")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://demo.portal.example.com/", http.NoBody)
	req.Host = "demo.portal.example.com"

	f.ServePortalHTMLWithSSR(rec, req, serv)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("content-type = %q, want %q", rec.Header().Get("Content-Type"), "text/html; charset=utf-8")
	}
	if rec.Header().Get("Cache-Control") != "no-cache, must-revalidate" {
		t.Fatalf("cache-control = %q, want %q", rec.Header().Get("Cache-Control"), "no-cache, must-revalidate")
	}

	body := rec.Body.String()
	if !strings.Contains(body, "<title>demo</title>") {
		t.Fatalf("body = %q, want lease name injected in title", body)
	}
	if !strings.Contains(body, `content="Demo service"`) {
		t.Fatalf("body = %q, want lease description injected", body)
	}
	if !strings.Contains(body, `content="https://cdn.example.com/demo.jpg"`) {
		t.Fatalf("body = %q, want lease thumbnail injected", body)
	}
}

func newTestFrontendWithDistFS(dist fstest.MapFS) *Frontend {
	f := NewFrontend("https://portal.example.com", "https://*.portal.example.com")
	f.distFS = dist
	return f
}

func newTestRelayServer(t *testing.T) *portal.RelayServer {
	t.Helper()

	cred, err := cryptoops.NewCredential()
	if err != nil {
		t.Fatalf("cryptoops.NewCredential: %v", err)
	}

	return portal.NewRelayServer(cred, []string{"127.0.0.1:4017"})
}
