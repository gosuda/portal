package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/types"
)

func TestParseTrustedProxyCIDRs(t *testing.T) {
	t.Run("parses and deduplicates", func(t *testing.T) {
		cidrs, err := manager.ParseTrustedProxyCIDRs("10.0.0.0/8, 10.0.0.0/8, 2001:db8::/32")
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		if len(cidrs) != 2 {
			t.Fatalf("expected 2 unique CIDRs, got %d", len(cidrs))
		}
	})

	t.Run("empty input", func(t *testing.T) {
		cidrs, err := manager.ParseTrustedProxyCIDRs("  ")
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		if len(cidrs) != 0 {
			t.Fatalf("expected no CIDRs for empty input, got %d", len(cidrs))
		}
	})

	t.Run("invalid cidr", func(t *testing.T) {
		if _, err := manager.ParseTrustedProxyCIDRs("not-a-cidr"); err == nil {
			t.Fatal("expected parse error for invalid CIDR input")
		}
	})
}

func newTestRelayServer(t *testing.T) *portal.RelayServer {
	t.Helper()

	serv, err := portal.NewRelayServer(
		context.Background(),
		[]string{"127.0.0.1:0"},
		":0",
		"portal.example.com",
		"",
		"",
	)
	if err != nil {
		t.Fatalf("new relay server: %v", err)
	}
	return serv
}

func TestServeAPIRemovesLegacyCompatResponses(t *testing.T) {
	t.Parallel()

	serv := newTestRelayServer(t)
	srv := serveAPI(":0", serv, nil, NewFrontend(), func() {})

	legacyPaths := []string{
		"/frontend/manifest.json",
		"/service-worker.js",
	}
	for _, p := range legacyPaths {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p, http.NoBody)
			rr := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rr, req)

			if rr.Code == http.StatusGone {
				t.Fatalf("legacy compat path %q should not return 410 compatibility shim", p)
			}

			body := strings.ToLower(rr.Body.String())
			if strings.Contains(body, "legacy webclient") || strings.Contains(body, "refresh required") {
				t.Fatalf("legacy compat marker should be removed for %q, got body %q", p, rr.Body.String())
			}
		})
	}
}

func TestSDKRegisterRejectsBannedIP(t *testing.T) {
	t.Parallel()

	serv := newTestRelayServer(t)
	ipManager := manager.NewIPManager()
	ipManager.BanIP("203.0.113.17")

	registry := &SDKRegistry{
		ipManager:         ipManager,
		trustProxyHeaders: false,
	}

	reqBody := strings.NewReader(`{"lease_id":"lease-ban","name":"test-lease","tls":true,"reverse_token":"token-1"}`)
	req := httptest.NewRequest(http.MethodPost, types.PathSDKRegister, reqBody)
	req.RemoteAddr = "203.0.113.17:45678"
	rr := httptest.NewRecorder()

	registry.handleRegister(rr, req, serv)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("unexpected status: got %d want %d", rr.Code, http.StatusForbidden)
	}

	var envelope types.APIRawEnvelope
	if err := json.NewDecoder(rr.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode register envelope: %v", err)
	}
	if envelope.OK {
		t.Fatal("expected banned IP registration to fail")
	}
	if envelope.Error == nil || envelope.Error.Message != "ip is banned" {
		t.Fatalf("unexpected error payload: %+v", envelope.Error)
	}
	if _, ok := serv.GetLeaseManager().GetLeaseByID("lease-ban"); ok {
		t.Fatal("banned registration should not create a lease")
	}
}

func TestSDKUnregisterCleansRouteAndReversePoolImmediately(t *testing.T) {
	t.Parallel()

	serv := newTestRelayServer(t)
	registry := &SDKRegistry{}

	lease := &portal.Lease{
		ID:           "lease-cleanup",
		Name:         "cleanup",
		TLS:          true,
		ReverseToken: "token-cleanup",
		Expires:      time.Now().Add(time.Minute),
	}
	if !serv.GetLeaseManager().UpdateLease(lease) {
		t.Fatal("failed to seed lease")
	}

	sniName := types.BuildSNIName(lease.Name, serv.BaseHost)
	if sniName == "" {
		t.Fatal("expected non-empty SNI name")
	}
	if err := serv.GetSNIRouter().RegisterRoute(sniName, lease.ID, lease.Name); err != nil {
		t.Fatalf("register route: %v", err)
	}

	local, peer := net.Pipe()
	defer peer.Close()
	conn := portal.NewReverseConn(local)
	defer conn.Close()
	if !serv.GetReverseHub().Offer(lease.ID, conn) {
		t.Fatal("failed to seed reverse pool")
	}

	reqBody := strings.NewReader(`{"lease_id":"lease-cleanup"}`)
	req := httptest.NewRequest(http.MethodPost, types.PathSDKUnregister, reqBody)
	rr := httptest.NewRecorder()
	registry.handleUnregister(rr, req, serv)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d want %d", rr.Code, http.StatusOK)
	}

	var envelope types.APIRawEnvelope
	if err := json.NewDecoder(rr.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode unregister envelope: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("expected successful unregister, got %+v", envelope)
	}
	if _, ok := serv.GetLeaseManager().GetLeaseByID(lease.ID); ok {
		t.Fatal("lease should be removed after unregister")
	}
	if _, ok := serv.GetSNIRouter().GetRouteByLeaseID(lease.ID); ok {
		t.Fatal("SNI route should be removed after unregister")
	}

	start := time.Now()
	_, err := serv.GetReverseHub().AcquireForTLS(lease.ID, 2*time.Second)
	if err == nil {
		t.Fatal("expected reverse pool to be removed after unregister")
	}
	if !strings.Contains(err.Error(), "no tunnel available") {
		t.Fatalf("unexpected acquire error after unregister: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("expected immediate cleanup, acquire took %v", elapsed)
	}
}
