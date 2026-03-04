package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/types"
)

func TestServeAPILegacyCompatPathsRemoved(t *testing.T) {
	prevPortalURL := flagPortalURL
	prevTrustProxyHeaders := flagTrustProxyHeaders
	flagPortalURL = "https://portal.example.com"
	flagTrustProxyHeaders = false
	t.Cleanup(func() {
		flagPortalURL = prevPortalURL
		flagTrustProxyHeaders = prevTrustProxyHeaders
	})

	serv, err := portal.NewRelayServer(
		context.Background(),
		nil,
		":0",
		types.PortalRootHost(flagPortalURL),
		"",
		"",
	)
	if err != nil {
		t.Fatalf("create relay server: %v", err)
	}

	frontend := NewFrontend()
	apiSrv := serveAPI(":0", serv, nil, frontend, func() {})
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = apiSrv.Shutdown(shutdownCtx)
	})

	cases := []struct {
		path            string
		forbiddenBodies []string
	}{
		{
			path:            "/frontend/manifest.json",
			forbiddenBodies: []string{"legacy webclient removed", "refresh required"},
		},
		{
			path:            "/frontend/app.js",
			forbiddenBodies: []string{"legacy webclient assets removed", "refresh required"},
		},
		{
			path:            "/service-worker.js",
			forbiddenBodies: []string{"portal-sw-cleanup-v2", "legacy sw cleanup worker"},
		},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Host = "portal.example.com"
		rec := httptest.NewRecorder()

		apiSrv.Handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusGone {
			t.Fatalf("%s unexpectedly returned %d (legacy compatibility path)", tc.path, rec.Code)
		}

		body := strings.ToLower(rec.Body.String())
		for _, forbidden := range tc.forbiddenBodies {
			if strings.Contains(body, strings.ToLower(forbidden)) {
				t.Fatalf("%s response still contains legacy compatibility text %q", tc.path, forbidden)
			}
		}
	}
}
