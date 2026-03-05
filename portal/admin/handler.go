package admin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/policy"
	"gosuda.org/portal/types"
)

var errInvalidLeaseID = errors.New("invalid lease ID")

type HandlerConfig struct {
	Service               *Service
	ServeAppStatic        func(http.ResponseWriter, *http.Request, string, *portal.RelayServer)
	ListLeases            func(*portal.RelayServer) any
	Stats                 func(*portal.RelayServer) map[string]any
	DecodeLeaseID         func(string) (string, bool)
	IsSecureRequest       func(*http.Request, bool) bool
	WriteAPIData          func(http.ResponseWriter, int, any)
	WriteAPIOK            func(http.ResponseWriter, int)
	WriteAPIError         func(http.ResponseWriter, int, string, string)
	WriteAPIErrorWithData func(http.ResponseWriter, int, string, string, any)
	TrustProxy            bool
}

// Handler routes /admin/* HTTP requests and delegates policy mutations to Service.
type Handler struct {
	service               *Service
	serveAppStatic        func(http.ResponseWriter, *http.Request, string, *portal.RelayServer)
	listLeases            func(*portal.RelayServer) any
	stats                 func(*portal.RelayServer) map[string]any
	decodeLeaseID         func(string) (string, bool)
	isSecureRequest       func(*http.Request, bool) bool
	writeAPIData          func(http.ResponseWriter, int, any)
	writeAPIOK            func(http.ResponseWriter, int)
	writeAPIError         func(http.ResponseWriter, int, string, string)
	writeAPIErrorWithData func(http.ResponseWriter, int, string, string, any)
	trustProxy            bool
}

func NewHandler(cfg HandlerConfig) *Handler {
	h := &Handler{
		service:               cfg.Service,
		trustProxy:            cfg.TrustProxy,
		serveAppStatic:        cfg.ServeAppStatic,
		listLeases:            cfg.ListLeases,
		stats:                 cfg.Stats,
		decodeLeaseID:         cfg.DecodeLeaseID,
		isSecureRequest:       cfg.IsSecureRequest,
		writeAPIData:          cfg.WriteAPIData,
		writeAPIOK:            cfg.WriteAPIOK,
		writeAPIError:         cfg.WriteAPIError,
		writeAPIErrorWithData: cfg.WriteAPIErrorWithData,
	}

	if h.serveAppStatic == nil {
		h.serveAppStatic = func(w http.ResponseWriter, r *http.Request, _ string, _ *portal.RelayServer) {
			http.NotFound(w, r)
		}
	}
	if h.listLeases == nil {
		h.listLeases = func(_ *portal.RelayServer) any { return []any{} }
	}
	if h.stats == nil {
		h.stats = func(serv *portal.RelayServer) map[string]any {
			count := 0
			if serv != nil && serv.GetLeaseManager() != nil {
				count = len(serv.GetLeaseManager().GetAllLeaseEntries())
			}
			return map[string]any{
				"leases_count": count,
			}
		}
	}
	if h.decodeLeaseID == nil {
		h.decodeLeaseID = decodeLeaseIDFallback
	}
	if h.isSecureRequest == nil {
		h.isSecureRequest = func(r *http.Request, _ bool) bool {
			return r != nil && r.TLS != nil
		}
	}
	if h.writeAPIData == nil {
		h.writeAPIData = func(w http.ResponseWriter, status int, data any) {
			writeDefaultEnvelope(w, status, types.APIEnvelope{OK: true, Data: data})
		}
	}
	if h.writeAPIOK == nil {
		h.writeAPIOK = func(w http.ResponseWriter, status int) {
			writeDefaultEnvelope(w, status, types.APIEnvelope{OK: true})
		}
	}
	if h.writeAPIError == nil {
		h.writeAPIError = func(w http.ResponseWriter, status int, code, message string) {
			writeDefaultEnvelope(w, status, types.APIEnvelope{
				OK: false,
				Error: &types.APIError{
					Code:    code,
					Message: message,
				},
			})
		}
	}
	if h.writeAPIErrorWithData == nil {
		h.writeAPIErrorWithData = func(w http.ResponseWriter, status int, code, message string, data any) {
			writeDefaultEnvelope(w, status, types.APIEnvelope{
				OK:   false,
				Data: data,
				Error: &types.APIError{
					Code:    code,
					Message: message,
				},
			})
		}
	}

	return h
}

func writeDefaultEnvelope(w http.ResponseWriter, status int, envelope types.APIEnvelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(envelope); err != nil {
		log.Error().Err(err).Msg("[Admin] Failed to encode API envelope")
	}
}

// HandleAdminRequest routes /admin/* requests.
func (h *Handler) HandleAdminRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer) {
	if h.service == nil {
		h.writeAPIError(w, http.StatusInternalServerError, "admin_service_unavailable", "admin service unavailable")
		return
	}

	route := strings.Trim(strings.TrimPrefix(r.URL.Path, types.PathAdminPrefix), "/")

	// Public routes (no authentication required)
	switch {
	case route == "login" && r.Method == http.MethodPost:
		h.handleLogin(w, r)
		return
	case route == "login":
		h.serveAppStatic(w, r, "", serv)
		return
	case route == "logout" && r.Method == http.MethodPost:
		h.handleLogout(w, r)
		return
	case route == "auth/status" && r.Method == http.MethodGet:
		h.handleAuthStatus(w, r)
		return
	}

	// Protected routes - require authentication
	if !h.service.IsAuthenticated(r) {
		// For page requests (no specific route), show login page.
		if route == "" {
			h.serveAppStatic(w, r, "", serv)
			return
		}
		// For API requests, return 401 envelope.
		h.writeAPIError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}

	switch {
	case route == "":
		h.serveAppStatic(w, r, "", serv)
	case route == "leases" && r.Method == http.MethodGet:
		h.writeAPIData(w, http.StatusOK, h.listLeases(serv))
	case route == "leases/banned" && r.Method == http.MethodGet:
		h.writeAPIData(w, http.StatusOK, serv.GetLeaseManager().GetBannedLeases())
	case route == "stats" && r.Method == http.MethodGet:
		h.writeAPIData(w, http.StatusOK, h.stats(serv))
	case route == "settings" && r.Method == http.MethodGet:
		h.handleGetSettings(w)
	case route == "settings/approval-mode":
		h.handleApprovalModeRequest(w, r, serv)
	case strings.HasPrefix(route, "leases/"):
		if !h.handleLeaseActionRouteRequest(w, r, serv, route) {
			http.NotFound(w, r)
		}
	case strings.HasPrefix(route, "ips/") && strings.HasSuffix(route, "/ban"):
		h.handleIPBanRequest(w, r, serv, route)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	authManager := h.service.GetAuthManager()
	clientIP := policy.ExtractClientIP(r, h.trustProxy)

	// Check if IP is locked.
	if authManager.IsIPLocked(clientIP) {
		remaining := authManager.GetLockRemainingSeconds(clientIP)
		h.writeAPIErrorWithData(
			w,
			http.StatusTooManyRequests,
			"auth_locked",
			"Too many failed attempts. Please try again later.",
			types.AdminLoginResponse{
				Locked:           true,
				RemainingSeconds: remaining,
			},
		)
		return
	}

	var req types.AdminLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}

	if !authManager.ValidateKey(req.Key) {
		// Record failed attempt.
		nowLocked := authManager.RecordFailedLogin(clientIP)
		log.Warn().Str("ip", clientIP).Bool("now_locked", nowLocked).Msg("[Admin] Failed login attempt")

		response := types.AdminLoginResponse{
			Locked: nowLocked,
		}
		if nowLocked {
			response.RemainingSeconds = authManager.GetLockRemainingSeconds(clientIP)
		}
		h.writeAPIErrorWithData(w, http.StatusUnauthorized, "invalid_key", "Invalid key", response)
		return
	}

	// Successful login.
	authManager.ResetFailedLogin(clientIP)
	token := authManager.CreateSession()
	secureCookie := h.isSecureRequest(r, h.trustProxy)

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   secureCookie,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400, // 24 hours
	})

	log.Info().Str("ip", clientIP).Msg("[Admin] Successful login")
	h.writeAPIData(w, http.StatusOK, types.AdminLoginResponse{Success: true})
}

func (h *Handler) handleApprovalModeRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer) {
	approveManager := h.service.GetApproveManager()
	switch r.Method {
	case http.MethodGet:
		h.writeAPIData(w, http.StatusOK, types.AdminApprovalModeResponse{
			ApprovalMode: string(approveManager.GetApprovalMode()),
		})
	case http.MethodPost:
		var req types.AdminApprovalModeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			h.writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
			return
		}
		mode := policy.Mode(req.Mode)
		if mode != policy.ModeAuto && mode != policy.ModeManual {
			h.writeAPIError(w, http.StatusBadRequest, "invalid_mode", "invalid mode (must be 'auto' or 'manual')")
			return
		}
		approveManager.SetApprovalMode(mode)
		h.service.SaveSettings(serv)
		log.Info().Str("mode", string(mode)).Msg("[Admin] Approval mode changed")
		h.writeAPIData(w, http.StatusOK, types.AdminApprovalModeResponse{
			ApprovalMode: string(mode),
		})
	default:
		h.writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	authManager := h.service.GetAuthManager()
	cookie, err := r.Cookie(CookieName)
	if err == nil && cookie.Value != "" {
		authManager.DeleteSession(cookie.Value)
	}
	secureCookie := h.isSecureRequest(r, h.trustProxy)

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		Secure:   secureCookie,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1, // Delete cookie.
	})

	h.writeAPIOK(w, http.StatusOK)
}

func (h *Handler) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	h.writeAPIData(w, http.StatusOK, types.AdminAuthStatusResponse{
		Authenticated: h.service.IsAuthenticated(r),
		AuthEnabled:   h.service.AuthEnabled(),
	})
}

func (h *Handler) parseLeaseActionRoute(route string) (leaseID, action string, err error) {
	parts := strings.Split(route, "/")
	if len(parts) != 3 || parts[0] != "leases" {
		return "", "", errors.New("route not found")
	}

	action = parts[2]
	switch action {
	case "ban", "bps", "approve", "deny":
	default:
		return "", "", errors.New("route not found")
	}

	var ok bool
	leaseID, ok = h.decodeLeaseID(parts[1])
	if !ok {
		return "", action, errInvalidLeaseID
	}

	return leaseID, action, nil
}

func (h *Handler) handleLeaseActionRouteRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, route string) bool {
	leaseID, action, err := h.parseLeaseActionRoute(route)
	if err != nil {
		if errors.Is(err, errInvalidLeaseID) {
			h.writeAPIError(w, http.StatusBadRequest, "invalid_lease_id", "invalid lease ID")
			return true
		}
		return false
	}

	switch action {
	case "ban":
		h.handleLeaseBanRequest(w, r, serv, leaseID)
	case "bps":
		h.handleLeaseBPSRequest(w, r, serv, leaseID)
	case "approve":
		h.handleLeaseApproveRequest(w, r, serv, leaseID)
	case "deny":
		h.handleLeaseDenyRequest(w, r, serv, leaseID)
	default:
		return false
	}

	return true
}

// handleLeaseToggleRequest is a generic helper for lease toggle actions (ban, approve, deny).
func (h *Handler) handleLeaseToggleRequest(
	w http.ResponseWriter,
	r *http.Request,
	serv *portal.RelayServer,
	leaseID string,
	onPost func(),
	onDelete func(),
	logMsgPost string,
	logMsgDelete string,
) {
	if strings.TrimSpace(leaseID) == "" {
		h.writeAPIError(w, http.StatusBadRequest, "invalid_lease_id", "invalid lease ID")
		return
	}

	switch r.Method {
	case http.MethodPost:
		onPost()
		h.service.SaveSettings(serv)
		if logMsgPost != "" {
			log.Info().Str("lease_id", leaseID).Msg(logMsgPost)
		}
		h.writeAPIOK(w, http.StatusOK)
	case http.MethodDelete:
		onDelete()
		h.service.SaveSettings(serv)
		if logMsgDelete != "" {
			log.Info().Str("lease_id", leaseID).Msg(logMsgDelete)
		}
		h.writeAPIOK(w, http.StatusOK)
	default:
		h.writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (h *Handler) handleLeaseBanRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, leaseID string) {
	h.handleLeaseToggleRequest(
		w, r, serv, leaseID,
		func() { serv.GetLeaseManager().BanLease(leaseID) },
		func() { serv.GetLeaseManager().UnbanLease(leaseID) },
		"", "",
	)
}

func (h *Handler) handleGetSettings(w http.ResponseWriter) {
	approveManager := h.service.GetApproveManager()
	h.writeAPIData(w, http.StatusOK, types.AdminSettingsResponse{
		ApprovalMode:   string(approveManager.GetApprovalMode()),
		ApprovedLeases: approveManager.GetApprovedLeases(),
		DeniedLeases:   approveManager.GetDeniedLeases(),
	})
}

func (h *Handler) handleLeaseApproveRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, leaseID string) {
	approveManager := h.service.GetApproveManager()
	h.handleLeaseToggleRequest(
		w, r, serv, leaseID,
		func() {
			approveManager.ApproveLease(leaseID)
			approveManager.UndenyLease(leaseID) // Remove from denied if exists.
		},
		func() { approveManager.RevokeLease(leaseID) },
		"[Admin] Lease approved",
		"[Admin] Lease approval revoked",
	)
}

func (h *Handler) handleLeaseDenyRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, leaseID string) {
	approveManager := h.service.GetApproveManager()
	h.handleLeaseToggleRequest(
		w, r, serv, leaseID,
		func() { approveManager.DenyLease(leaseID) },
		func() { approveManager.UndenyLease(leaseID) },
		"[Admin] Lease denied",
		"[Admin] Lease denial removed",
	)
}

func (h *Handler) handleLeaseBPSRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, leaseID string) {
	if strings.TrimSpace(leaseID) == "" {
		h.writeAPIError(w, http.StatusBadRequest, "invalid_lease_id", "invalid lease ID")
		return
	}

	bpsManager := h.service.GetBPSManager()
	switch r.Method {
	case http.MethodPost:
		var req types.AdminBPSRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			h.writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
			return
		}
		if bpsManager == nil {
			h.writeAPIError(w, http.StatusInternalServerError, "bps_manager_unavailable", "bps manager not initialized")
			return
		}
		oldBPS := bpsManager.GetBPSLimit(leaseID)
		bpsManager.SetBPSLimit(leaseID, req.BPS)
		log.Info().
			Str("lease_id", leaseID).
			Int64("old_bps", oldBPS).
			Int64("new_bps", req.BPS).
			Msg("[Admin] BPS limit updated")
		h.service.SaveSettings(serv)
		h.writeAPIOK(w, http.StatusOK)
	case http.MethodDelete:
		if bpsManager == nil {
			h.writeAPIError(w, http.StatusInternalServerError, "bps_manager_unavailable", "bps manager not initialized")
			return
		}
		oldBPS := bpsManager.GetBPSLimit(leaseID)
		bpsManager.SetBPSLimit(leaseID, 0)
		log.Info().
			Str("lease_id", leaseID).
			Int64("old_bps", oldBPS).
			Msg("[Admin] BPS limit removed (now unlimited)")
		h.service.SaveSettings(serv)
		h.writeAPIOK(w, http.StatusOK)
	default:
		h.writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (h *Handler) handleIPBanRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, route string) {
	// Route format: ips/{ip}/ban.
	parts := strings.Split(route, "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}

	ip := parts[1]
	if ip == "" {
		h.writeAPIError(w, http.StatusBadRequest, "invalid_ip", "invalid IP address")
		return
	}

	ipManager := h.service.GetIPManager()
	if ipManager == nil {
		h.writeAPIError(w, http.StatusInternalServerError, "ip_manager_unavailable", "ip manager not initialized")
		return
	}

	switch r.Method {
	case http.MethodPost:
		ipManager.BanIP(ip)
		h.service.SaveSettings(serv)
		log.Info().Str("ip", ip).Msg("[Admin] IP banned")
		h.writeAPIOK(w, http.StatusOK)
	case http.MethodDelete:
		ipManager.UnbanIP(ip)
		h.service.SaveSettings(serv)
		log.Info().Str("ip", ip).Msg("[Admin] IP unbanned")
		h.writeAPIOK(w, http.StatusOK)
	default:
		h.writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func decodeLeaseIDFallback(encoded string) (string, bool) {
	idBytes, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		idBytes, err = base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return "", false
		}
	}
	return string(idBytes), true
}
