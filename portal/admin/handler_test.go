package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/policy"
	"gosuda.org/portal/types"
)

func TestHandleAdminRequestLoginSuccessSetsSessionCookie(t *testing.T) {
	service := NewService(policy.NewAuthenticator("test-secret"))
	handler := newTestHandler(t, service, true, nil)

	req := httptest.NewRequest(http.MethodPost, types.PathAdminPrefix+"/login", strings.NewReader(`{"key":"test-secret"}`))
	req.RemoteAddr = "203.0.113.10:1234"
	rec := httptest.NewRecorder()

	handler.HandleAdminRequest(rec, req, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	envelope := decodeEnvelope(t, rec)
	if !envelope.OK {
		t.Fatalf("expected OK response, got %+v", envelope)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("expected admin session cookie")
	}
	cookie := cookies[0]
	if cookie.Name != CookieName {
		t.Fatalf("expected cookie name %q, got %q", CookieName, cookie.Name)
	}
	if cookie.Path != "/admin" {
		t.Fatalf("expected cookie path /admin, got %q", cookie.Path)
	}
	if !cookie.HttpOnly {
		t.Fatalf("expected HttpOnly cookie")
	}
	if !cookie.Secure {
		t.Fatalf("expected Secure cookie")
	}
	if cookie.MaxAge != 86400 {
		t.Fatalf("expected MaxAge 86400, got %d", cookie.MaxAge)
	}
}

func TestHandleAdminRequestProtectedRouteUnauthorized(t *testing.T) {
	service := NewService(policy.NewAuthenticator("test-secret"))
	handler := newTestHandler(t, service, false, func(_ *portal.RelayServer) any {
		t.Fatalf("list leases should not be called for unauthorized request")
		return nil
	})

	req := httptest.NewRequest(http.MethodGet, types.PathAdminPrefix+"/leases", nil)
	rec := httptest.NewRecorder()

	handler.HandleAdminRequest(rec, req, nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
	envelope := decodeEnvelope(t, rec)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "unauthorized" {
		t.Fatalf("expected unauthorized API error, got %+v", envelope)
	}
}

func TestHandleAdminRequestApprovalModeInvalidMode(t *testing.T) {
	service := NewService(policy.NewAuthenticator("test-secret"))
	handler := newTestHandler(t, service, false, nil)
	token := service.GetAuthManager().CreateSession()

	req := httptest.NewRequest(http.MethodPost, types.PathAdminPrefix+"/settings/approval-mode", strings.NewReader(`{"mode":"invalid"}`))
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	rec := httptest.NewRecorder()

	handler.HandleAdminRequest(rec, req, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	envelope := decodeEnvelope(t, rec)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "invalid_mode" {
		t.Fatalf("expected invalid_mode API error, got %+v", envelope)
	}
}

func TestHandleAdminRequestLeaseActionInvalidLeaseID(t *testing.T) {
	service := NewService(policy.NewAuthenticator("test-secret"))
	handler := newTestHandler(t, service, false, nil)
	token := service.GetAuthManager().CreateSession()

	req := httptest.NewRequest(http.MethodPost, types.PathAdminPrefix+"/leases/not!base64/ban", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	rec := httptest.NewRecorder()

	handler.HandleAdminRequest(rec, req, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	envelope := decodeEnvelope(t, rec)
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "invalid_lease_id" {
		t.Fatalf("expected invalid_lease_id API error, got %+v", envelope)
	}
}

func newTestHandler(t *testing.T, service *Service, secure bool, listLeases func(*portal.RelayServer) any) *Handler {
	t.Helper()

	if listLeases == nil {
		listLeases = func(_ *portal.RelayServer) any { return []any{} }
	}

	return NewHandler(HandlerConfig{
		Service:    service,
		TrustProxy: false,
		ServeAppStatic: func(w http.ResponseWriter, _ *http.Request, _ string, _ *portal.RelayServer) {
			w.WriteHeader(http.StatusOK)
		},
		ListLeases: listLeases,
		IsSecureRequest: func(_ *http.Request, _ bool) bool {
			return secure
		},
		WriteAPIData:          writeTestAPIData,
		WriteAPIOK:            writeTestAPIOK,
		WriteAPIError:         writeTestAPIError,
		WriteAPIErrorWithData: writeTestAPIErrorWithData,
	})
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) types.APIEnvelope {
	t.Helper()
	var envelope types.APIEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("failed to decode API envelope: %v", err)
	}
	return envelope
}

func writeTestAPIData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(types.APIEnvelope{OK: true, Data: data})
}

func writeTestAPIOK(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(types.APIEnvelope{OK: true})
}

func writeTestAPIError(w http.ResponseWriter, status int, code, message string) {
	writeTestAPIErrorWithData(w, status, code, message, nil)
}

func writeTestAPIErrorWithData(w http.ResponseWriter, status int, code, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(types.APIEnvelope{
		OK:   false,
		Data: data,
		Error: &types.APIError{
			Code:    code,
			Message: message,
		},
	})
}
