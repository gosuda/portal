package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/types"
)

func attachPeerLeaseCertificate(req *http.Request, leaseID string) {
	leaseURI, _ := url.Parse(controlPlaneLeaseURIPfx + leaseID)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{
			{
				NotBefore:   time.Now().Add(-1 * time.Minute),
				NotAfter:    time.Now().Add(1 * time.Hour),
				URIs:        []*url.URL{leaseURI},
				ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
				Subject: pkix.Name{
					CommonName: controlPlaneCertCNPrefix + leaseID,
				},
			},
		},
	}
}

func decodeAPIRawEnvelope(t *testing.T, rec *httptest.ResponseRecorder) types.APIRawEnvelope {
	t.Helper()

	var envelope types.APIRawEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode API envelope: %v (body=%q)", err, rec.Body.String())
	}
	return envelope
}

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
	attachPeerLeaseCertificate(req, payload.LeaseID)
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
	attachPeerLeaseCertificate(req, payload.LeaseID)
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

func TestSDKRegistryHandleRenewRejectsBannedIP(t *testing.T) {
	serv := newRegistryTestRelayServer(t)
	ipManager := manager.NewIPManager()
	ipManager.BanIP("203.0.113.22")
	registry := &SDKRegistry{ipManager: ipManager}

	lease := &portal.Lease{
		ID:           "lease-renew-ban",
		Name:         "tenant",
		TLS:          true,
		ReverseToken: "renew-token",
		Expires:      time.Now().Add(30 * time.Second),
	}
	if ok := serv.GetLeaseManager().UpdateLease(lease); !ok {
		t.Fatal("failed to seed lease")
	}

	payload := types.RenewRequest{
		LeaseID:      lease.ID,
		ReverseToken: lease.ReverseToken,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal renew payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, types.PathSDKRenew, bytes.NewReader(body))
	attachPeerLeaseCertificate(req, lease.ID)
	req.RemoteAddr = "203.0.113.22:45000"
	rec := httptest.NewRecorder()

	registry.handleRenew(rec, req, serv)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("handleRenew status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	envelope := decodeAPIRawEnvelope(t, rec)
	if envelope.Error == nil || envelope.Error.Code != "ip_banned" {
		t.Fatalf("unexpected renew ip_banned payload: %+v", envelope.Error)
	}
}

func TestSDKRegistryHandleRegisterRequiresClientCertificate(t *testing.T) {
	serv := newRegistryTestRelayServer(t)
	registry := &SDKRegistry{}

	payload := types.RegisterRequest{
		LeaseID:      "lease-register-cert-required",
		Name:         "tenant",
		TLS:          true,
		ReverseToken: "reverse-token",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal register payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, types.PathSDKRegister, bytes.NewReader(body))
	req.TLS = &tls.ConnectionState{}
	rec := httptest.NewRecorder()
	registry.handleRegister(rec, req, serv)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("handleRegister status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	envelope := decodeAPIRawEnvelope(t, rec)
	if envelope.OK {
		t.Fatalf("expected register rejection, got %+v", envelope)
	}
	if envelope.Error == nil || envelope.Error.Code != "client_cert_required" {
		t.Fatalf("unexpected register rejection payload: %+v", envelope.Error)
	}
}

func TestSDKRegistryHandleUnregisterRequiresReverseToken(t *testing.T) {
	serv := newRegistryTestRelayServer(t)
	registry := &SDKRegistry{}

	lease := &portal.Lease{
		ID:           "lease-unregister-token-required",
		Name:         "tenant",
		TLS:          true,
		ReverseToken: "reverse-token",
		Expires:      time.Now().Add(30 * time.Second),
	}
	if ok := serv.GetLeaseManager().UpdateLease(lease); !ok {
		t.Fatal("failed to seed lease")
	}

	payload := types.UnregisterRequest{
		LeaseID: lease.ID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal unregister payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, types.PathSDKUnregister, bytes.NewReader(body))
	attachPeerLeaseCertificate(req, lease.ID)
	rec := httptest.NewRecorder()
	registry.handleUnregister(rec, req, serv)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("handleUnregister status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	envelope := decodeAPIRawEnvelope(t, rec)
	if envelope.OK {
		t.Fatalf("expected unregister rejection, got %+v", envelope)
	}
	if envelope.Error == nil || envelope.Error.Code != "missing_reverse_token" {
		t.Fatalf("unexpected unregister rejection payload: %+v", envelope.Error)
	}
}

func TestSDKRegistryHandleUnregisterWithValidIdentity(t *testing.T) {
	serv := newRegistryTestRelayServer(t)
	registry := &SDKRegistry{}

	lease := &portal.Lease{
		ID:           "lease-unregister-success",
		Name:         "tenant",
		TLS:          true,
		ReverseToken: "reverse-token",
		Expires:      time.Now().Add(30 * time.Second),
	}
	if ok := serv.GetLeaseManager().UpdateLease(lease); !ok {
		t.Fatal("failed to seed lease")
	}

	payload := types.UnregisterRequest{
		LeaseID:      lease.ID,
		ReverseToken: lease.ReverseToken,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal unregister payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, types.PathSDKUnregister, bytes.NewReader(body))
	attachPeerLeaseCertificate(req, lease.ID)
	rec := httptest.NewRecorder()
	registry.handleUnregister(rec, req, serv)

	if rec.Code != http.StatusOK {
		t.Fatalf("handleUnregister status = %d, want %d", rec.Code, http.StatusOK)
	}
	if _, ok := serv.GetLeaseManager().GetLeaseByID(lease.ID); ok {
		t.Fatalf("lease %q should be removed after unregister", lease.ID)
	}
}

func TestSDKRegistryHandleUnregisterRejectsTokenMismatch(t *testing.T) {
	serv := newRegistryTestRelayServer(t)
	registry := &SDKRegistry{}

	lease := &portal.Lease{
		ID:           "lease-unregister-token-mismatch",
		Name:         "tenant",
		TLS:          true,
		ReverseToken: "correct-token",
		Expires:      time.Now().Add(30 * time.Second),
	}
	if ok := serv.GetLeaseManager().UpdateLease(lease); !ok {
		t.Fatal("failed to seed lease")
	}

	payload := types.UnregisterRequest{
		LeaseID:      lease.ID,
		ReverseToken: "wrong-token",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal unregister payload: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, types.PathSDKUnregister, bytes.NewReader(body))
	attachPeerLeaseCertificate(req, lease.ID)
	rec := httptest.NewRecorder()

	registry.handleUnregister(rec, req, serv)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("handleUnregister status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	envelope := decodeAPIRawEnvelope(t, rec)
	if envelope.Error == nil || envelope.Error.Code != "unauthorized" {
		t.Fatalf("unexpected unregister mismatch payload: %+v", envelope.Error)
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
	attachPeerLeaseCertificate(req, "lease-connect-ban")
	req.RemoteAddr = "203.0.113.22:45000"
	req.Header.Set(portal.ReverseConnectTokenHeader, "reverse-token")
	rec := httptest.NewRecorder()

	registry.handleConnect(rec, req, serv)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("handleConnect status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	envelope := decodeAPIRawEnvelope(t, rec)
	if envelope.OK {
		t.Fatalf("expected banned IP response to fail, got %+v", envelope)
	}
	if envelope.Error == nil || envelope.Error.Code != "ip_banned" || envelope.Error.Message != "ip is banned" {
		t.Fatalf("unexpected banned IP error payload: %+v", envelope.Error)
	}
}

func TestSDKRegistryHandleConnectRejectsMissingLease(t *testing.T) {
	serv := newRegistryTestRelayServer(t)
	registry := &SDKRegistry{}

	req := httptest.NewRequest(http.MethodGet, types.PathSDKConnect+"?lease_id=missing-lease", http.NoBody)
	req.Header.Set(portal.ReverseConnectTokenHeader, "reverse-token")
	attachPeerLeaseCertificate(req, "missing-lease")
	rec := httptest.NewRecorder()

	registry.handleConnect(rec, req, serv)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("handleConnect status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	envelope := decodeAPIRawEnvelope(t, rec)
	if envelope.Error == nil || envelope.Error.Code != "lease_not_found" {
		t.Fatalf("unexpected lease_not_found payload: %+v", envelope.Error)
	}
}

func TestSDKRegistryHandleConnectRejectsCertLeaseMismatch(t *testing.T) {
	serv := newRegistryTestRelayServer(t)
	registry := &SDKRegistry{}

	lease := &portal.Lease{
		ID:           "lease-cert-mismatch",
		Name:         "tenant",
		TLS:          true,
		ReverseToken: "reverse-token",
		Expires:      time.Now().Add(30 * time.Second),
	}
	if ok := serv.GetLeaseManager().UpdateLease(lease); !ok {
		t.Fatal("failed to seed lease")
	}

	req := httptest.NewRequest(http.MethodGet, types.PathSDKConnect+"?lease_id="+lease.ID, http.NoBody)
	req.Header.Set(portal.ReverseConnectTokenHeader, lease.ReverseToken)
	attachPeerLeaseCertificate(req, "other-lease")
	rec := httptest.NewRecorder()

	registry.handleConnect(rec, req, serv)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("handleConnect status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	envelope := decodeAPIRawEnvelope(t, rec)
	if envelope.Error == nil || envelope.Error.Code != "cert_lease_mismatch" {
		t.Fatalf("unexpected cert_lease_mismatch payload: %+v", envelope.Error)
	}
}

func TestSDKRegistryHandleConnectRequiresTLS(t *testing.T) {
	serv := newRegistryTestRelayServer(t)
	registry := &SDKRegistry{}
	serv.GetLeaseManager().UpdateLease(&portal.Lease{
		ID:           "lease-connect-tls",
		Name:         "lease-connect-tls",
		ReverseToken: "reverse-token",
		Expires:      time.Now().Add(time.Hour),
		TLS:          true,
	})

	req := httptest.NewRequest(http.MethodGet, types.PathSDKConnect+"?lease_id=lease-connect-tls", http.NoBody)
	req.Header.Set(portal.ReverseConnectTokenHeader, "reverse-token")
	rec := httptest.NewRecorder()

	registry.handleConnect(rec, req, serv)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("handleConnect status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	envelope := decodeAPIRawEnvelope(t, rec)
	if envelope.OK {
		t.Fatalf("expected client_cert_required response to fail, got %+v", envelope)
	}
	if envelope.Error == nil || envelope.Error.Code != "client_cert_required" {
		t.Fatalf("unexpected client_cert_required payload: %+v", envelope.Error)
	}
}

func TestSDKRegistryHandleConnectMissingLeaseIDReturnsEnvelope(t *testing.T) {
	serv := newRegistryTestRelayServer(t)
	registry := &SDKRegistry{}

	req := httptest.NewRequest(http.MethodGet, types.PathSDKConnect, http.NoBody)
	attachPeerLeaseCertificate(req, "lease-missing")
	req.Header.Set(portal.ReverseConnectTokenHeader, "reverse-token")
	rec := httptest.NewRecorder()

	registry.handleConnect(rec, req, serv)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("handleConnect status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	envelope := decodeAPIRawEnvelope(t, rec)
	if envelope.OK {
		t.Fatalf("expected missing lease_id response to fail, got %+v", envelope)
	}
	if envelope.Error == nil || envelope.Error.Code != "missing_lease_id" || envelope.Error.Message != "lease_id is required" {
		t.Fatalf("unexpected missing lease_id payload: %+v", envelope.Error)
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
	req.TLS = &tls.ConnectionState{}
	req.Header.Set(portal.ReverseConnectTokenHeader, "reverse-token")
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()

	registry.handleConnect(rec, req, serv)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("handleConnect status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	envelope := decodeAPIRawEnvelope(t, rec)
	if envelope.OK {
		t.Fatalf("expected websocket rejection to fail, got %+v", envelope)
	}
	if envelope.Error == nil || envelope.Error.Code != "unsupported_transport" {
		t.Fatalf("unexpected websocket rejection payload: %+v", envelope.Error)
	}
	if !strings.Contains(strings.ToLower(envelope.Error.Message), "websocket") {
		t.Fatalf("unexpected websocket rejection message: %+v", envelope.Error)
	}
}
