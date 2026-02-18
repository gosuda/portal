package main

import (
	"embed"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
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

		require.Equal(t, http.StatusMethodNotAllowed, rec.Code, "status")
		require.Equal(t, expectedAllowGetOrHead, rec.Header().Get("Allow"), "Allow header")
	})

	t.Run("GetDefaultShellScript", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tunnel", http.NoBody)

		serveTunnelScript(rec, req, portalURL)

		require.Equal(t, http.StatusOK, rec.Code, "status")
		require.Equal(t, "text/x-shellscript", rec.Header().Get("Content-Type"), "Content-Type header")
		require.Equal(t, `inline; filename="tunnel.sh"`, rec.Header().Get("Content-Disposition"), "Content-Disposition header")

		body := rec.Body.String()
		require.Contains(t, body, "#!/usr/bin/env sh", "body missing shell shebang")
		require.Contains(t, body, portalURL, "body missing portal URL")
		require.Contains(t, body, "tunnel/bin/$TUNNEL_OS-$TUNNEL_ARCH", "body missing non-windows tunnel path")
	})

	t.Run("GetWindowsFromQueryParam", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tunnel?os=windows", http.NoBody)

		serveTunnelScript(rec, req, portalURL)

		require.Equal(t, http.StatusOK, rec.Code, "status")
		require.Equal(t, "text/plain", rec.Header().Get("Content-Type"), "Content-Type header")
		require.Equal(t, `inline; filename="tunnel.ps1"`, rec.Header().Get("Content-Disposition"), "Content-Disposition header")

		body := rec.Body.String()
		require.Contains(t, body, `$ErrorActionPreference = "Stop"`, "body missing PowerShell marker")
		require.Contains(t, body, "windows-$TunnelArch", "body missing windows tunnel path")
	})

	t.Run("GetWindowsFromUserAgentFallback", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tunnel", http.NoBody)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

		serveTunnelScript(rec, req, portalURL)

		require.Equal(t, http.StatusOK, rec.Code, "status")
		require.Equal(t, "text/plain", rec.Header().Get("Content-Type"), "Content-Type header")
		require.Contains(t, rec.Body.String(), `$ErrorActionPreference = "Stop"`, "body missing PowerShell marker")
	})

	t.Run("HeadReturnsHeadersAndNoBody", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodHead, "/tunnel", http.NoBody)

		serveTunnelScript(rec, req, portalURL)

		require.Equal(t, http.StatusOK, rec.Code, "status")
		require.Equal(t, "text/x-shellscript", rec.Header().Get("Content-Type"), "Content-Type header")
		require.Equal(t, 0, rec.Body.Len(), "body length")
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

		require.Equal(t, http.StatusMethodNotAllowed, rec.Code, "status")
		require.Equal(t, expectedAllowGetOrHead, rec.Header().Get("Allow"), "Allow header")
	})

	t.Run("UnknownSlugReturnsNotFound", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tunnel/bin/unknown-slug", http.NoBody)

		serveTunnelBinary(rec, req)

		require.Equal(t, http.StatusNotFound, rec.Code, "status")
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

				require.Equal(t, http.StatusNotFound, rec.Code, "status")
			})
		}
	})
}
