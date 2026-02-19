package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	require.Equal(t, admin, f.admin, "frontend admin pointer not assigned")
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
		require.True(t, ok, "update lease %q failed", id)
	}

	updateLease("lease-visible", "", `{"description":"Visible lease"}`, []string{"tcp"}, 101)
	admin.GetApproveManager().ApproveLease("lease-visible")
	admin.GetBPSManager().SetBPSLimit("lease-visible", 2048)
	visibleLease, ok := leaseManager.GetLeaseByID("lease-visible")
	require.True(t, ok, "visible lease not found")
	visibleLease.LastSeen = time.Now()

	updateLease("lease-hidden", "hidden", `{"hide":true}`, []string{"http/1.1"}, 102)
	admin.GetApproveManager().ApproveLease("lease-hidden")

	updateLease("lease-stale", "stale", `{"description":"stale"}`, nil, 103)
	admin.GetApproveManager().ApproveLease("lease-stale")
	staleLease, ok := leaseManager.GetLeaseByID("lease-stale")
	require.True(t, ok, "stale lease not found")
	staleLease.LastSeen = time.Now().Add(-4 * time.Minute)

	updateLease("lease-unapproved", "pending", `{"description":"pending"}`, nil, 104)

	updateLease("lease-banned", "banned", `{"description":"banned"}`, nil, 105)
	admin.GetApproveManager().ApproveLease("lease-banned")
	leaseManager.BanLease("lease-banned")

	rows := convertLeaseEntriesToRows(serv, admin, f.portalURL, f.portalAppURL)
	require.Len(t, rows, 1, "len(rows) should be 1")

	row := rows[0]
	expectedFallbackName := "(" + "unnamed" + ")"
	require.Equal(t, "lease-visible", row.Peer, "row.Peer mismatch")
	require.Equal(t, expectedFallbackName, row.Name, "row.Name mismatch")
	require.Equal(t, "tcp", row.Kind, "row.Kind mismatch")
	require.Equal(t, int64(2048), row.BPS, "row.BPS mismatch")
	require.Equal(t, "//.portal.example.com/", row.Link, "row.Link mismatch")
	require.False(t, row.StaleRed, "row.StaleRed should be false for recent lease")
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
				assert.Contains(t, got, w)
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

			require.Equal(t, tt.wantStatus, rec.Code, "status mismatch")
			if tt.wantBody != "" {
				require.Equal(t, tt.wantBody, rec.Body.String(), "body mismatch")
			}
			if tt.wantContentType != "" {
				require.Equal(t, tt.wantContentType, rec.Header().Get("Content-Type"), "content-type mismatch")
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

		require.Equal(t, http.StatusBadRequest, rec.Code, "status mismatch")
		require.Contains(t, rec.Body.String(), "Invalid path", "body should contain Invalid path")
	})

	t.Run("root path serves SSR html", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/portal.html": {Data: []byte(portalHTML)},
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/app/", http.NoBody)

		f.ServeAppStatic(rec, req, "/", nil)

		require.Equal(t, http.StatusOK, rec.Code, "status mismatch")
		require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"), "content-type mismatch")
		require.Equal(t, "no-cache, must-revalidate", rec.Header().Get("Cache-Control"), "cache-control mismatch")

		body := rec.Body.String()
		require.Contains(t, body, "Portal Proxy Gateway", "body should contain default OG title")
		require.Contains(t, body, "__SSR_DATA__", "body should contain SSR data script marker")
	})

	t.Run("existing static file", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/assets/app.js": {Data: []byte("console.log('app');")},
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/app/assets/app.js", http.NoBody)

		f.ServeAppStatic(rec, req, "assets/app.js", nil)

		require.Equal(t, http.StatusOK, rec.Code, "status mismatch")
		require.Equal(t, "application/javascript", rec.Header().Get("Content-Type"), "content-type mismatch")
		require.Equal(t, "public, max-age=3600", rec.Header().Get("Cache-Control"), "cache-control mismatch")
		require.Equal(t, "console.log('app');", rec.Body.String(), "body mismatch")
	})

	t.Run("missing file falls back to SSR", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/portal.html": {Data: []byte(portalHTML)},
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/app/missing.js", http.NoBody)

		f.ServeAppStatic(rec, req, "missing.js", nil)

		require.Equal(t, http.StatusOK, rec.Code, "status mismatch")
		require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"), "content-type mismatch")
		require.Equal(t, "no-cache, must-revalidate", rec.Header().Get("Cache-Control"), "cache-control mismatch")
		require.Contains(t, rec.Body.String(), "Portal Proxy Gateway", "body should contain default OG metadata")
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

		require.Equal(t, http.StatusBadRequest, rec.Code, "status mismatch")
		require.Contains(t, rec.Body.String(), "Invalid path", "body should contain Invalid path")
	})

	t.Run("portal.jpg special headers", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/portal.jpg": {Data: []byte("jpg-bytes")},
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/portal.jpg", http.NoBody)

		f.ServePortalStatic(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "status mismatch")
		require.Equal(t, "public, max-age=604800", rec.Header().Get("Cache-Control"), "cache-control mismatch")
		require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"), "content-type mismatch")
		require.Equal(t, "jpg-bytes", rec.Body.String(), "body mismatch")
	})

	t.Run("portal.mp4 special headers", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/portal.mp4": {Data: []byte("mp4-bytes")},
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/portal.mp4", http.NoBody)

		f.ServePortalStatic(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "status mismatch")
		require.Equal(t, "public, max-age=604800", rec.Header().Get("Cache-Control"), "cache-control mismatch")
		require.Equal(t, "video/mp4", rec.Header().Get("Content-Type"), "content-type mismatch")
		require.Equal(t, "mp4-bytes", rec.Body.String(), "body mismatch")
	})

	t.Run("generic missing path falls back to portal.html", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/portal.html": {Data: []byte("<html>portal fallback</html>")},
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/some/deep/route", http.NoBody)

		f.ServePortalStatic(rec, req)

		require.Equal(t, http.StatusOK, rec.Code, "status mismatch")
		require.Equal(t, "public, max-age=3600", rec.Header().Get("Cache-Control"), "cache-control mismatch")
		require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"), "content-type mismatch")
		require.Equal(t, "<html>portal fallback</html>", rec.Body.String(), "body mismatch")
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

	require.Equal(t, http.StatusOK, rec.Code, "status mismatch")
	require.Equal(t, "public, max-age=3600", rec.Header().Get("Cache-Control"), "cache-control mismatch")
	require.Equal(t, "text/css", rec.Header().Get("Content-Type"), "content-type mismatch")
	require.Equal(t, "body { color: #111; }", rec.Body.String(), "body mismatch")
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

		require.Equal(t, http.StatusOK, rec.Code, "status mismatch")
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"), "content-type mismatch")
		require.Equal(t, `{"ok":true}`, rec.Body.String(), "body mismatch")
	})

	t.Run("existing file respects explicit content type", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/assets/blob.bin": {Data: []byte("blob")},
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/assets/blob.bin", http.NoBody)
		f.serveStaticFileWithFallback(rec, req, "assets/blob.bin", "application/octet-stream")

		require.Equal(t, http.StatusOK, rec.Code, "status mismatch")
		require.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"), "content-type mismatch")
		require.Equal(t, "blob", rec.Body.String(), "body mismatch")
	})

	t.Run("missing file falls back to portal html", func(t *testing.T) {
		t.Parallel()

		f := newTestFrontendWithDistFS(fstest.MapFS{
			"dist/app/portal.html": {Data: []byte("<html>fallback</html>")},
		})

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/missing", http.NoBody)
		f.serveStaticFileWithFallback(rec, req, "does-not-exist", "application/octet-stream")

		require.Equal(t, http.StatusOK, rec.Code, "status mismatch")
		require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"), "content-type mismatch")
		require.Equal(t, "<html>fallback</html>", rec.Body.String(), "body mismatch")
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
	require.True(t, ok, "expected lease update to succeed")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://demo.portal.example.com/", http.NoBody)
	req.Host = "demo.portal.example.com"

	f.ServePortalHTMLWithSSR(rec, req, serv)

	require.Equal(t, http.StatusOK, rec.Code, "status mismatch")
	require.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"), "content-type mismatch")
	require.Equal(t, "no-cache, must-revalidate", rec.Header().Get("Cache-Control"), "cache-control mismatch")

	body := rec.Body.String()
	require.Contains(t, body, "<title>demo</title>", "body should contain lease name injected in title")
	require.Contains(t, body, `content="Demo service"`, "body should contain lease description injected")
	require.Contains(t, body, `content="https://cdn.example.com/demo.jpg"`, "body should contain lease thumbnail injected")
}

func newTestFrontendWithDistFS(dist fstest.MapFS) *Frontend {
	f := NewFrontend("https://portal.example.com", "https://*.portal.example.com")
	f.distFS = dist
	return f
}

func newTestRelayServer(t *testing.T) *portal.RelayServer {
	t.Helper()

	cred, err := cryptoops.NewCredential()
	require.NoError(t, err, "cryptoops.NewCredential failed")

	return portal.NewRelayServer(cred, []string{"127.0.0.1:4017"})
}
