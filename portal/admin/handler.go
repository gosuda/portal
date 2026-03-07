package admin

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/gosuda/portal/v2/portal"
	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/types"
)

const cookieName = "portal_admin"

type Handler struct {
	auth           *policy.Authenticator
	runtime        *policy.Runtime
	server         *portal.Server
	settings       *stateStore
	serveAppStatic func(http.ResponseWriter, *http.Request, string)
	buildLeaseRows func(*portal.Server, bool) []LeaseRow
	trustProxy     bool
}

func NewHandler(portalURL, secret, settingsPath string, trustProxy bool, serveAppStatic func(http.ResponseWriter, *http.Request, string)) *Handler {
	h := &Handler{
		auth:     policy.NewAuthenticator(strings.TrimSpace(secret)),
		runtime:  policy.NewRuntime(),
		settings: newStateStore(settingsPath),
		buildLeaseRows: func(serv *portal.Server, includeAdmin bool) []LeaseRow {
			return BuildLeaseRows(serv, includeAdmin, portalURL)
		},
		trustProxy: trustProxy,
	}
	if serveAppStatic != nil {
		h.serveAppStatic = serveAppStatic
	} else {
		h.serveAppStatic = func(w http.ResponseWriter, r *http.Request, _ string) {
			http.NotFound(w, r)
		}
	}
	return h
}

func (h *Handler) Bind(server *portal.Server) {
	h.server = server
}

func (h *Handler) Runtime() *policy.Runtime {
	return h.runtime
}

func (h *Handler) LoadSettings() error {
	return h.settings.Load(h.runtime)
}

func (h *Handler) HandleRequest(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(strings.TrimSpace(r.URL.Path), "/")
	if path == "" {
		path = types.PathRoot
	}

	switch path {
	case types.PathAdmin:
		h.serveAppStatic(w, r, "")
		return
	case types.PathAdminLogin:
		if r.Method == http.MethodGet {
			h.serveAppStatic(w, r, "")
			return
		}
		if r.Method == http.MethodPost {
			h.handleLogin(w, r)
			return
		}
	case types.PathAdminLogout:
		h.handleLogout(w, r)
		return
	case types.PathAdminAuthStatus:
		h.handleAuthStatus(w, r)
		return
	}

	if !h.isAuthenticated(r) {
		writeAPIError(w, http.StatusUnauthorized, types.APIErrorCodeUnauthorized, "unauthorized")
		return
	}

	switch path {
	case types.PathAdminLeases:
		h.handleLeases(w, r)
	case types.PathAdminBanned:
		h.handleBannedLeases(w, r)
	case types.PathAdminSettings:
		h.handleSettings(w, r)
	case types.PathAdminApproval:
		h.handleApprovalMode(w, r)
	default:
		switch {
		case strings.HasPrefix(path, types.PathAdminLeasesPrefix):
			h.handleLeaseAction(w, r, path)
		case strings.HasPrefix(path, types.PathAdminIPsPrefix):
			h.handleIPBan(w, r, path)
		default:
			http.NotFound(w, r)
		}
	}
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !h.auth.AuthEnabled() {
		writeAPIError(w, http.StatusServiceUnavailable, types.APIErrorCodeAuthDisabled, "admin authentication is not configured")
		return
	}

	clientIP := policy.ExtractClientIP(r, h.trustProxy)
	if h.auth.IsIPLocked(clientIP) {
		writeAPIErrorWithData(w, http.StatusTooManyRequests, types.APIErrorCodeAuthLocked, "Too many failed attempts. Please try again later.", types.AdminLoginResponse{
			Locked:           true,
			RemainingSeconds: h.auth.LockRemainingSeconds(clientIP),
		})
		return
	}

	var req types.AdminLoginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "invalid request body")
		return
	}
	if !h.auth.ValidateKey(req.Key) {
		locked := h.auth.RecordFailedLogin(clientIP)
		resp := types.AdminLoginResponse{Locked: locked}
		if locked {
			resp.RemainingSeconds = h.auth.LockRemainingSeconds(clientIP)
		}
		writeAPIErrorWithData(w, http.StatusUnauthorized, types.APIErrorCodeInvalidKey, "Invalid key", resp)
		return
	}

	h.auth.ResetFailedLogin(clientIP)
	token, err := h.auth.CreateSession()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, types.APIErrorCodeSessionCreateFailed, "failed to create admin session")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     types.PathAdmin,
		HttpOnly: true,
		Secure:   policy.IsSecureForwardedRequest(r, h.trustProxy),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
	})
	writeAPIData(w, http.StatusOK, types.AdminLoginResponse{Success: true})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}

	if cookie, err := r.Cookie(cookieName); err == nil && cookie.Value != "" {
		h.auth.DeleteSession(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     types.PathAdmin,
		HttpOnly: true,
		Secure:   policy.IsSecureForwardedRequest(r, h.trustProxy),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	writeAPIOK(w, http.StatusOK)
}

func (h *Handler) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}
	writeAPIData(w, http.StatusOK, types.AdminAuthStatusResponse{
		Authenticated: h.isAuthenticated(r),
		AuthEnabled:   h.auth.AuthEnabled(),
	})
}

func (h *Handler) handleLeases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}
	writeAPIData(w, http.StatusOK, h.buildLeaseRows(h.server, true))
}

func (h *Handler) handleBannedLeases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}
	writeAPIData(w, http.StatusOK, h.runtime.BannedLeases())
}

func (h *Handler) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}
	approver := h.runtime.Approver()
	writeAPIData(w, http.StatusOK, types.AdminSettingsResponse{
		ApprovalMode:   string(approver.Mode()),
		ApprovedLeases: approver.ApprovedLeases(),
		DeniedLeases:   approver.DeniedLeases(),
	})
}

func (h *Handler) handleApprovalMode(w http.ResponseWriter, r *http.Request) {
	approver := h.runtime.Approver()
	switch r.Method {
	case http.MethodGet:
		writeAPIData(w, http.StatusOK, types.AdminApprovalModeResponse{ApprovalMode: string(approver.Mode())})
	case http.MethodPost:
		var req types.AdminApprovalModeRequest
		if err := decodeJSON(w, r, &req); err != nil {
			writeAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "invalid request body")
			return
		}
		if err := approver.SetMode(policy.Mode(req.Mode)); err != nil {
			writeAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidMode, "invalid mode (must be 'auto' or 'manual')")
			return
		}
		_ = h.settings.Save(h.runtime)
		writeAPIData(w, http.StatusOK, types.AdminApprovalModeResponse{ApprovalMode: string(approver.Mode())})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleLeaseAction(w http.ResponseWriter, r *http.Request, path string) {
	rest := strings.TrimPrefix(path, types.PathAdminLeasesPrefix)
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}

	leaseID, ok := decodeLeaseID(parts[0])
	if !ok {
		writeAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidLeaseID, "invalid lease ID")
		return
	}

	switch parts[1] {
	case "ban":
		h.handleLeaseBan(w, r, leaseID)
	case "bps":
		writeAPIError(w, http.StatusNotImplemented, types.APIErrorCodeFeatureUnavailable, "bps control is not enabled in this build")
	case "approve":
		h.handleLeaseApproval(w, r, leaseID)
	case "deny":
		h.handleLeaseDenial(w, r, leaseID)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleLeaseBan(w http.ResponseWriter, r *http.Request, leaseID string) {
	switch r.Method {
	case http.MethodPost:
		h.runtime.BanLease(leaseID)
		_ = h.settings.Save(h.runtime)
		writeAPIOK(w, http.StatusOK)
	case http.MethodDelete:
		h.runtime.UnbanLease(leaseID)
		_ = h.settings.Save(h.runtime)
		writeAPIOK(w, http.StatusOK)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleLeaseApproval(w http.ResponseWriter, r *http.Request, leaseID string) {
	approver := h.runtime.Approver()
	switch r.Method {
	case http.MethodPost:
		approver.Approve(leaseID)
		approver.Undeny(leaseID)
		_ = h.settings.Save(h.runtime)
		writeAPIOK(w, http.StatusOK)
	case http.MethodDelete:
		approver.Revoke(leaseID)
		_ = h.settings.Save(h.runtime)
		writeAPIOK(w, http.StatusOK)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleLeaseDenial(w http.ResponseWriter, r *http.Request, leaseID string) {
	approver := h.runtime.Approver()
	switch r.Method {
	case http.MethodPost:
		approver.Deny(leaseID)
		_ = h.settings.Save(h.runtime)
		writeAPIOK(w, http.StatusOK)
	case http.MethodDelete:
		approver.Undeny(leaseID)
		_ = h.settings.Save(h.runtime)
		writeAPIOK(w, http.StatusOK)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) handleIPBan(w http.ResponseWriter, r *http.Request, path string) {
	if !strings.HasSuffix(path, "/ban") {
		http.NotFound(w, r)
		return
	}
	rawIP := strings.TrimSuffix(strings.TrimPrefix(path, types.PathAdminIPsPrefix), "/ban")
	rawIP = strings.Trim(rawIP, "/")
	if net.ParseIP(rawIP) == nil {
		writeAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidIP, "invalid IP address")
		return
	}

	ipFilter := h.runtime.IPFilter()
	switch r.Method {
	case http.MethodPost:
		ipFilter.BanIP(rawIP)
		_ = h.settings.Save(h.runtime)
		writeAPIOK(w, http.StatusOK)
	case http.MethodDelete:
		ipFilter.UnbanIP(rawIP)
		_ = h.settings.Save(h.runtime)
		writeAPIOK(w, http.StatusOK)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) isAuthenticated(r *http.Request) bool {
	if !h.auth.AuthEnabled() {
		return false
	}
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return h.auth.ValidateSession(cookie.Value)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}

func decodeLeaseID(encoded string) (string, bool) {
	idBytes, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		idBytes, err = base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return "", false
		}
	}
	return string(idBytes), true
}

func writeAPIData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(types.APIEnvelope[any]{OK: true, Data: data})
}

func writeAPIOK(w http.ResponseWriter, status int) {
	writeAPIData(w, status, map[string]any{})
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(types.APIEnvelope[any]{
		OK:    false,
		Error: &types.APIError{Code: code, Message: message},
	})
}

func writeAPIErrorWithData(w http.ResponseWriter, status int, code, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(types.APIEnvelope[any]{
		OK:    false,
		Data:  data,
		Error: &types.APIError{Code: code, Message: message},
	})
}
