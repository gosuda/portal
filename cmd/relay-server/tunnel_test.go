package main

import (
	"embed"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeTunnelScript(t *testing.T) {
	const (
		portalURL              = "https://portal.example.test"
		expectedAllowGetOrHead = http.MethodGet + ", " + http.MethodHead
	)

	t.Run("RejectsNonGetHead", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/tunnel", http.NoBody)

		serveTunnelScript(rec, req, portalURL)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
		if got := rec.Header().Get("Allow"); got != expectedAllowGetOrHead {
			t.Fatalf("Allow = %q, want %q", got, expectedAllowGetOrHead)
		}
	})

	t.Run("GetDefaultShellScript", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tunnel", http.NoBody)

		serveTunnelScript(rec, req, portalURL)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/x-shellscript" {
			t.Fatalf("Content-Type = %q, want %q", got, "text/x-shellscript")
		}
		if got := rec.Header().Get("Content-Disposition"); got != `inline; filename="tunnel.sh"` {
			t.Fatalf("Content-Disposition = %q, want %q", got, `inline; filename="tunnel.sh"`)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "#!/usr/bin/env sh") {
			t.Fatalf("body missing shell shebang:\n%s", body)
		}
		if !strings.Contains(body, portalURL) {
			t.Fatalf("body missing portal URL %q", portalURL)
		}
		if !strings.Contains(body, "tunnel/bin/$TUNNEL_OS-$TUNNEL_ARCH") {
			t.Fatalf("body missing non-windows tunnel path")
		}
	})

	t.Run("GetWindowsFromQueryParam", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tunnel?os=windows", http.NoBody)

		serveTunnelScript(rec, req, portalURL)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/plain" {
			t.Fatalf("Content-Type = %q, want %q", got, "text/plain")
		}
		if got := rec.Header().Get("Content-Disposition"); got != `inline; filename="tunnel.ps1"` {
			t.Fatalf("Content-Disposition = %q, want %q", got, `inline; filename="tunnel.ps1"`)
		}

		body := rec.Body.String()
		if !strings.Contains(body, `$ErrorActionPreference = "Stop"`) {
			t.Fatalf("body missing PowerShell marker")
		}
		if !strings.Contains(body, "windows-$TunnelArch") {
			t.Fatalf("body missing windows tunnel path")
		}
	})

	t.Run("GetWindowsFromUserAgentFallback", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tunnel", http.NoBody)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

		serveTunnelScript(rec, req, portalURL)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/plain" {
			t.Fatalf("Content-Type = %q, want %q", got, "text/plain")
		}
		if !strings.Contains(rec.Body.String(), `$ErrorActionPreference = "Stop"`) {
			t.Fatalf("body missing PowerShell marker")
		}
	})

	t.Run("HeadReturnsHeadersAndNoBody", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodHead, "/tunnel", http.NoBody)

		serveTunnelScript(rec, req, portalURL)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/x-shellscript" {
			t.Fatalf("Content-Type = %q, want %q", got, "text/x-shellscript")
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("body length = %d, want 0", rec.Body.Len())
		}
	})
}

func TestServeTunnelBinary(t *testing.T) {
	const (
		expectedAllowGetOrHead = http.MethodGet + ", " + http.MethodHead
		knownSlugPath          = "/tunnel/bin/linux-amd64"
	)

	t.Run("RejectsNonGetHead", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, knownSlugPath, http.NoBody)

		serveTunnelBinary(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
		if got := rec.Header().Get("Allow"); got != expectedAllowGetOrHead {
			t.Fatalf("Allow = %q, want %q", got, expectedAllowGetOrHead)
		}
	})

	t.Run("UnknownSlugReturnsNotFound", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tunnel/bin/unknown-slug", http.NoBody)

		serveTunnelBinary(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("KnownSlugMissingBinaryReturnsNotFound", func(t *testing.T) {
		originalDistFS := distFS
		distFS = embed.FS{}
		t.Cleanup(func() {
			distFS = originalDistFS
		})

		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method, func(t *testing.T) {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(method, knownSlugPath, http.NoBody)

				serveTunnelBinary(rec, req)

				if rec.Code != http.StatusNotFound {
					t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
				}
			})
		}
	})
}
