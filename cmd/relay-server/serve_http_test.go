package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAppHost       = "portal.example.com"
	testSubdomainHost = "demo.portal.example.com"
	testPortalURL     = "https://portal.example.com"
	testPortalAppURL  = "https://*.portal.example.com"
)

type serveHTTPTestHarness struct {
	srv           *http.Server
	shutdownCalls *atomic.Int32
}

func newServeHTTPTestHarness(t *testing.T, noIndex bool, certHash []byte) *serveHTTPTestHarness {
	t.Helper()

	frontend := newTestFrontendWithDistFS(fstest.MapFS{
		"dist/app/portal.html": {
			Data: []byte(`<html><head><title>[%OG_TITLE%]</title><meta name="description" content="[%OG_DESCRIPTION%]"></head><body>portal</body></html>`),
		},
	})

	serv := newTestRelayServer(t)
	shutdownCalls := &atomic.Int32{}
	srv := serveHTTP(
		":0",
		serv,
		nil,
		frontend,
		noIndex,
		certHash,
		testPortalAppURL,
		testPortalURL,
		func() {
			shutdownCalls.Add(1)
		},
	)

	h := &serveHTTPTestHarness{
		srv:           srv,
		shutdownCalls: shutdownCalls,
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		err := h.srv.Shutdown(ctx)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			assert.NoError(t, err, "srv.Shutdown() error")
		}
		assert.Equal(t, int32(0), h.shutdownCalls.Load(), "shutdown callback should not be called")
	})

	return h
}

func (h *serveHTTPTestHarness) serve(host, targetPath string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://"+host+targetPath, http.NoBody)
	req.Host = host
	rec := httptest.NewRecorder()
	h.srv.Handler.ServeHTTP(rec, req)
	return rec
}

func TestServeHTTP_RobotsTxtNoIndex(t *testing.T) {
	t.Parallel()

	h := newServeHTTPTestHarness(t, true, nil)

	rec := h.serve(testAppHost, "/robots.txt")

	require.Equal(t, http.StatusOK, rec.Code, "status")
	require.Equal(t, "text/plain", rec.Header().Get("Content-Type"), "content-type")
	require.Equal(t, "User-agent: *\nDisallow: /\n", rec.Body.String(), "body")
}

func TestServeHTTP_Healthz(t *testing.T) {
	t.Parallel()

	h := newServeHTTPTestHarness(t, false, nil)

	rec := h.serve(testAppHost, "/healthz")

	require.Equal(t, http.StatusOK, rec.Code, "status")
	require.Equal(t, "{\"status\":\"ok\"}", rec.Body.String(), "body")
}

func TestServeHTTP_RelayOverHTTPReturnsUpgradeRequired(t *testing.T) {
	t.Parallel()

	h := newServeHTTPTestHarness(t, false, nil)

	rec := h.serve(testAppHost, "/relay")

	require.Equal(t, http.StatusUpgradeRequired, rec.Code, "status")
	require.Contains(t, rec.Body.String(), "WebTransport (HTTP/3) required", "body should contain WebTransport error message")
}

func TestServeHTTP_CertHashRoutePresence(t *testing.T) {
	t.Parallel()

	t.Run("present when hash configured", func(t *testing.T) {
		t.Parallel()

		h := newServeHTTPTestHarness(t, false, []byte{0x01, 0xab})

		rec := h.serve(testAppHost, "/cert-hash")

		require.Equal(t, http.StatusOK, rec.Code, "status")
		require.Equal(t, "application/json", rec.Header().Get("Content-Type"), "content-type")
		require.Equal(t, `{"algorithm":"sha-256","hash":"01ab"}`, rec.Body.String(), "body")
	})

	t.Run("absent when hash is not configured", func(t *testing.T) {
		t.Parallel()

		h := newServeHTTPTestHarness(t, false, nil)

		rec := h.serve(testAppHost, "/cert-hash")

		require.Equal(t, http.StatusOK, rec.Code, "status")
		assert.NotContains(t, rec.Body.String(), `"algorithm":"sha-256"`, "body should not contain cert-hash JSON payload")
		require.Contains(t, rec.Body.String(), `id="__SSR_DATA__"`, "body should contain app SSR fallback when /cert-hash route is absent")
	})
}

func TestServeHTTP_HostBasedRouting(t *testing.T) {
	t.Parallel()

	h := newServeHTTPTestHarness(t, false, nil)

	appRec := h.serve(testAppHost, "/")
	require.Equal(t, http.StatusOK, appRec.Code, "app status")
	require.Contains(t, appRec.Body.String(), `id="__SSR_DATA__"`, "app body should contain app mux SSR script marker")

	subdomainRec := h.serve(testSubdomainHost, "/")
	require.Equal(t, http.StatusOK, subdomainRec.Code, "subdomain status")
	assert.NotContains(t, subdomainRec.Body.String(), `id="__SSR_DATA__"`, "subdomain body should not contain app SSR script marker")
	require.Contains(t, subdomainRec.Body.String(), "<title>Portal Proxy Gateway</title>", "subdomain body should contain portal HTML with OG metadata injected")
}
