package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/types"
)

func newRegistryTestRelayServer(t *testing.T) *portal.RelayServer {
	t.Helper()

	serv, err := portal.NewRelayServer(context.Background(), nil, ":0", "example.com", "", "")
	if err != nil {
		t.Fatalf("create relay server: %v", err)
	}
	return serv
}

func TestSDKRegistryHandleRegisterTrimsReverseToken(t *testing.T) {
	serv := newRegistryTestRelayServer(t)
	registry := &SDKRegistry{}

	originalPortalURL := flagPortalURL
	flagPortalURL = "https://portal.example.com"
	t.Cleanup(func() {
		flagPortalURL = originalPortalURL
	})

	payload := types.RegisterRequest{
		LeaseID:      "lease-register-token-trim",
		Name:         "tenant",
		TLS:          true,
		ReverseToken: " reverse-token ",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal register payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, types.PathSDKRegister, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	registry.handleRegister(rec, req, serv)

	var envelope types.APIRawEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode register envelope: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("register response not successful: %+v", envelope)
	}

	var response types.RegisterResponse
	if err := json.Unmarshal(envelope.Data, &response); err != nil {
		t.Fatalf("decode register response data: %v", err)
	}
	if !response.Success {
		t.Fatalf("register response not successful: %+v", response)
	}

	entry, ok := serv.GetLeaseManager().GetLeaseByID(payload.LeaseID)
	if !ok || entry == nil || entry.Lease == nil {
		t.Fatalf("registered lease not found: %q", payload.LeaseID)
	}
	if got := entry.Lease.ReverseToken; got != "reverse-token" {
		t.Fatalf("stored reverse token mismatch: got %q want %q", got, "reverse-token")
	}
}

func TestSDKRegistryHandleRenewAcceptsTrimmedReverseToken(t *testing.T) {
	serv := newRegistryTestRelayServer(t)
	registry := &SDKRegistry{}

	lease := &portal.Lease{
		ID:           "lease-renew-token-trim",
		Name:         "tenant",
		TLS:          true,
		ReverseToken: "reverse-token",
		Expires:      time.Now().Add(30 * time.Second),
	}
	if ok := serv.GetLeaseManager().UpdateLease(lease); !ok {
		t.Fatal("failed to seed lease")
	}

	payload := types.RenewRequest{
		LeaseID:      lease.ID,
		ReverseToken: " reverse-token ",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal renew payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, types.PathSDKRenew, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	registry.handleRenew(rec, req, serv)

	var envelope types.APIRawEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode renew envelope: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("renew response not successful: %+v", envelope)
	}
}

func TestSDKRegistryHandleConnectRejectsBannedIP(t *testing.T) {
	serv := newRegistryTestRelayServer(t)
	ipManager := manager.NewIPManager()
	ipManager.BanIP("203.0.113.22")
	registry := &SDKRegistry{
		ipManager:         ipManager,
		trustProxyHeaders: false,
	}

	req := httptest.NewRequest(http.MethodGet, types.PathSDKConnect+"?lease_id=lease-connect-ban", http.NoBody)
	req.RemoteAddr = "203.0.113.22:45000"
	req.Header.Set(portal.ReverseConnectTokenHeader, "reverse-token")
	rec := httptest.NewRecorder()

	registry.handleConnect(rec, req, serv)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("handleConnect status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if !strings.Contains(rec.Body.String(), "ip is banned") {
		t.Fatalf("expected banned ip error body, got %q", rec.Body.String())
	}
}

func TestIsWebSocketUpgrade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{name: "empty", header: "", want: false},
		{name: "websocket lowercase", header: "websocket", want: true},
		{name: "websocket mixed case", header: "WebSocket", want: true},
		{name: "other upgrade", header: "h2c", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, types.PathSDKConnect, http.NoBody)
			if tt.header != "" {
				req.Header.Set("Upgrade", tt.header)
			}
			if got := isWebSocketUpgrade(req); got != tt.want {
				t.Fatalf("isWebSocketUpgrade(%q)=%v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

func TestSDKRegistryHandleConnectRejectsWebSocketUpgrade(t *testing.T) {
	serv := newRegistryTestRelayServer(t)
	registry := &SDKRegistry{}

	req := httptest.NewRequest(http.MethodGet, types.PathSDKConnect+"?lease_id=lease-websocket", http.NoBody)
	req.Header.Set(portal.ReverseConnectTokenHeader, "reverse-token")
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()

	registry.handleConnect(rec, req, serv)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("handleConnect status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "websocket") {
		t.Fatalf("expected websocket rejection body, got %q", rec.Body.String())
	}
}
