package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gosuda/portal/v2/portal"
	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/types"
)

func TestLoginAndProtectedActions(t *testing.T) {
	t.Parallel()

	handler := NewHandler("secret-key", filepath.Join(t.TempDir(), "admin_settings.json"), false, func(w http.ResponseWriter, _ *http.Request, _ string) {
		w.WriteHeader(http.StatusOK)
	})
	server, err := portal.NewServer(portal.ServerConfig{PortalURL: "https://portal.example.com"})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	handler.Bind(server)

	loginRecorder := httptest.NewRecorder()
	loginRequest := httptest.NewRequest(http.MethodPost, types.PathAdminLogin, bytes.NewBufferString(`{"key":"secret-key"}`))
	loginRequest.RemoteAddr = "127.0.0.1:1234"
	handler.HandleRequest(loginRecorder, loginRequest)

	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d", loginRecorder.Code, http.StatusOK)
	}
	loginResponse := decodeEnvelope[types.AdminLoginResponse](t, loginRecorder)
	if !loginResponse.Success {
		t.Fatalf("login success = false, want true")
	}
	cookies := loginRecorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("login cookies = 0, want at least 1")
	}

	authRecorder := httptest.NewRecorder()
	authRequest := httptest.NewRequest(http.MethodGet, types.PathAdminAuthStatus, nil)
	authRequest.RemoteAddr = "127.0.0.1:1234"
	authRequest.AddCookie(cookies[0])
	handler.HandleRequest(authRecorder, authRequest)
	authStatus := decodeEnvelope[types.AdminAuthStatusResponse](t, authRecorder)
	if !authStatus.Authenticated || !authStatus.AuthEnabled {
		t.Fatalf("auth status = %+v, want authenticated + auth enabled", authStatus)
	}

	snapshotRecorder := httptest.NewRecorder()
	snapshotRequest := httptest.NewRequest(http.MethodGet, types.PathAdminSnapshot, nil)
	snapshotRequest.RemoteAddr = "127.0.0.1:1234"
	snapshotRequest.AddCookie(cookies[0])
	handler.HandleRequest(snapshotRecorder, snapshotRequest)
	if snapshotRecorder.Code != http.StatusOK {
		t.Fatalf("snapshot status = %d, want %d", snapshotRecorder.Code, http.StatusOK)
	}
	snapshot := decodeEnvelope[types.AdminSnapshotResponse](t, snapshotRecorder)
	if snapshot.ApprovalMode != string(policy.ModeAuto) {
		t.Fatalf("snapshot approval mode = %q, want %q", snapshot.ApprovalMode, policy.ModeAuto)
	}
	if len(snapshot.Leases) != 0 {
		t.Fatalf("snapshot leases len = %d, want 0", len(snapshot.Leases))
	}

	legacyLeasesRecorder := httptest.NewRecorder()
	legacyLeasesRequest := httptest.NewRequest(http.MethodGet, types.PathAdminLeases, nil)
	legacyLeasesRequest.RemoteAddr = "127.0.0.1:1234"
	legacyLeasesRequest.AddCookie(cookies[0])
	handler.HandleRequest(legacyLeasesRecorder, legacyLeasesRequest)
	if legacyLeasesRecorder.Code != http.StatusNotFound {
		t.Fatalf("legacy leases status = %d, want %d", legacyLeasesRecorder.Code, http.StatusNotFound)
	}

	legacyBannedRecorder := httptest.NewRecorder()
	legacyBannedRequest := httptest.NewRequest(http.MethodGet, "/admin/leases/banned", nil)
	legacyBannedRequest.RemoteAddr = "127.0.0.1:1234"
	legacyBannedRequest.AddCookie(cookies[0])
	handler.HandleRequest(legacyBannedRecorder, legacyBannedRequest)
	if legacyBannedRecorder.Code != http.StatusNotFound {
		t.Fatalf("legacy banned status = %d, want %d", legacyBannedRecorder.Code, http.StatusNotFound)
	}

	legacySettingsRecorder := httptest.NewRecorder()
	legacySettingsRequest := httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	legacySettingsRequest.RemoteAddr = "127.0.0.1:1234"
	legacySettingsRequest.AddCookie(cookies[0])
	handler.HandleRequest(legacySettingsRecorder, legacySettingsRequest)
	if legacySettingsRecorder.Code != http.StatusNotFound {
		t.Fatalf("legacy settings status = %d, want %d", legacySettingsRecorder.Code, http.StatusNotFound)
	}

	legacyApprovalGetRecorder := httptest.NewRecorder()
	legacyApprovalGetRequest := httptest.NewRequest(http.MethodGet, types.PathAdminApproval, nil)
	legacyApprovalGetRequest.RemoteAddr = "127.0.0.1:1234"
	legacyApprovalGetRequest.AddCookie(cookies[0])
	handler.HandleRequest(legacyApprovalGetRecorder, legacyApprovalGetRequest)
	if legacyApprovalGetRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("legacy approval GET status = %d, want %d", legacyApprovalGetRecorder.Code, http.StatusMethodNotAllowed)
	}

	approvalRecorder := httptest.NewRecorder()
	approvalRequest := httptest.NewRequest(http.MethodPost, types.PathAdminApproval, bytes.NewBufferString(`{"mode":"manual"}`))
	approvalRequest.RemoteAddr = "127.0.0.1:1234"
	approvalRequest.AddCookie(cookies[0])
	handler.HandleRequest(approvalRecorder, approvalRequest)
	if approvalRecorder.Code != http.StatusOK {
		t.Fatalf("approval status = %d, want %d", approvalRecorder.Code, http.StatusOK)
	}
	if got := server.PolicyRuntime().Approver().Mode(); got != policy.ModeManual {
		t.Fatalf("approval mode = %q, want %q", got, policy.ModeManual)
	}

	ipBanRecorder := httptest.NewRecorder()
	ipBanRequest := httptest.NewRequest(http.MethodPost, types.PathAdminIPsPrefix+"203.0.113.10/ban", nil)
	ipBanRequest.RemoteAddr = "127.0.0.1:1234"
	ipBanRequest.AddCookie(cookies[0])
	handler.HandleRequest(ipBanRecorder, ipBanRequest)
	if ipBanRecorder.Code != http.StatusOK {
		t.Fatalf("ip ban status = %d, want %d", ipBanRecorder.Code, http.StatusOK)
	}
	if !server.PolicyRuntime().IPFilter().IsIPBanned("203.0.113.10") {
		t.Fatalf("IsIPBanned() = false, want true")
	}
}

func decodeEnvelope[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()

	var envelope types.APIEnvelope[T]
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
		t.Fatalf("Decode envelope error = %v", err)
	}
	if !envelope.OK {
		t.Fatalf("envelope not OK: %+v", envelope)
	}
	return envelope.Data
}
