package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"gosuda.org/portal/portal/policy"
	"gosuda.org/portal/types"
)

func TestLoginAndProtectedActions(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Config{
		Secret:       "secret-key",
		SettingsPath: filepath.Join(t.TempDir(), "admin_settings.json"),
		ServeAppStatic: func(w http.ResponseWriter, _ *http.Request, _ string) {
			w.WriteHeader(http.StatusOK)
		},
	})

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

	approvalRecorder := httptest.NewRecorder()
	approvalRequest := httptest.NewRequest(http.MethodPost, types.PathAdminApproval, bytes.NewBufferString(`{"mode":"manual"}`))
	approvalRequest.RemoteAddr = "127.0.0.1:1234"
	approvalRequest.AddCookie(cookies[0])
	handler.HandleRequest(approvalRecorder, approvalRequest)
	if approvalRecorder.Code != http.StatusOK {
		t.Fatalf("approval status = %d, want %d", approvalRecorder.Code, http.StatusOK)
	}
	if got := handler.runtime.Approver().Mode(); got != policy.ModeManual {
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
	if !handler.runtime.IPFilter().IsIPBanned("203.0.113.10") {
		t.Fatalf("IsIPBanned() = false, want true")
	}
}

func decodeEnvelope[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()

	var envelope types.APIEnvelope
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
		t.Fatalf("Decode envelope error = %v", err)
	}
	if !envelope.OK {
		t.Fatalf("envelope not OK: %+v", envelope)
	}
	data, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatalf("Marshal envelope data error = %v", err)
	}

	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal envelope data error = %v", err)
	}
	return out
}
