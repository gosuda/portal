package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
)

const adminCookieName = "portal_admin"

// Admin manages approval state and persistence for relay-server.
type Admin struct {
	settingsPath string
	settingsMu   sync.Mutex

	approveManager *manager.ApproveManager
	bpsManager     *manager.BPSManager
	ipManager      *manager.IPManager
	authManager    *manager.AuthManager

	frontend *Frontend
}

func NewAdmin(defaultLeaseBPS int64, frontend *Frontend, authManager *manager.AuthManager) *Admin {
	bpsManager := manager.NewBPSManager()
	if defaultLeaseBPS > 0 {
		bpsManager.SetDefaultBPS(defaultLeaseBPS)
	}
	return &Admin{
		settingsPath:   "admin_settings.json",
		approveManager: manager.NewApproveManager(),
		bpsManager:     bpsManager,
		ipManager:      manager.NewIPManager(),
		authManager:    authManager,
		frontend:       frontend,
	}
}

// GetApproveManager exposes the approval manager.
func (a *Admin) GetApproveManager() *manager.ApproveManager {
	return a.approveManager
}

// GetBPSManager exposes the BPS manager.
func (a *Admin) GetBPSManager() *manager.BPSManager {
	return a.bpsManager
}

// GetIPManager exposes the IP manager.
func (a *Admin) GetIPManager() *manager.IPManager {
	return a.ipManager
}

// adminSettings stores persistent admin configuration
type adminSettings struct {
	BannedLeases   []string             `json:"banned_leases"`
	BPSLimits      map[string]int64     `json:"bps_limits"`
	ApprovalMode   manager.ApprovalMode `json:"approval_mode"`
	ApprovedLeases []string             `json:"approved_leases,omitempty"`
	DeniedLeases   []string             `json:"denied_leases,omitempty"`
	BannedIPs      []string             `json:"banned_ips,omitempty"`
}

func (a *Admin) SetSettingsPath(path string) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.settingsPath = path
}

func (a *Admin) SaveSettings(serv *portal.RelayServer) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()

	lm := serv.GetLeaseManager()

	bannedBytes := lm.GetBannedLeases()
	banned := make([]string, len(bannedBytes))
	for i, b := range bannedBytes {
		banned[i] = string(b)
	}

	bpsLimits := map[string]int64{}
	if a.bpsManager != nil {
		bpsLimits = a.bpsManager.GetAllBPSLimits()
	}

	var bannedIPs []string
	if a.ipManager != nil {
		bannedIPs = a.ipManager.GetBannedIPs()
	}

	settings := adminSettings{
		BannedLeases:   banned,
		BPSLimits:      bpsLimits,
		ApprovalMode:   a.approveManager.GetApprovalMode(),
		ApprovedLeases: a.approveManager.GetApprovedLeases(),
		DeniedLeases:   a.approveManager.GetDeniedLeases(),
		BannedIPs:      bannedIPs,
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("[Admin] Failed to marshal admin settings")
		return
	}

	dir := filepath.Dir(a.settingsPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Error().Err(err).Msg("[Admin] Failed to create settings directory")
			return
		}
	}

	if err := os.WriteFile(a.settingsPath, data, 0644); err != nil {
		log.Error().Err(err).Msg("[Admin] Failed to save admin settings")
		return
	}

	log.Debug().Str("path", a.settingsPath).Msg("[Admin] Saved admin settings")
}

func (a *Admin) LoadSettings(serv *portal.RelayServer) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()

	data, err := os.ReadFile(a.settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug().Msg("[Admin] No admin settings file found, starting fresh")
			return
		}
		log.Error().Err(err).Msg("[Admin] Failed to read admin settings")
		return
	}

	var settings adminSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		log.Error().Err(err).Msg("[Admin] Failed to parse admin settings")
		return
	}

	lm := serv.GetLeaseManager()

	for _, leaseID := range settings.BannedLeases {
		lm.BanLease(leaseID)
	}

	for leaseID, bps := range settings.BPSLimits {
		if a.bpsManager != nil {
			a.bpsManager.SetBPSLimit(leaseID, bps)
		}
	}

	if settings.ApprovalMode != "" {
		a.approveManager.SetApprovalMode(settings.ApprovalMode)
	}

	for _, leaseID := range settings.ApprovedLeases {
		a.approveManager.ApproveLease(leaseID)
	}

	for _, leaseID := range settings.DeniedLeases {
		a.approveManager.DenyLease(leaseID)
	}

	if a.ipManager != nil && len(settings.BannedIPs) > 0 {
		a.ipManager.SetBannedIPs(settings.BannedIPs)
	}

	log.Info().
		Int("banned_count", len(settings.BannedLeases)).
		Int("bps_limits_count", len(settings.BPSLimits)).
		Str("approval_mode", string(a.approveManager.GetApprovalMode())).
		Int("approved_count", len(settings.ApprovedLeases)).
		Int("denied_count", len(settings.DeniedLeases)).
		Int("banned_ips_count", len(settings.BannedIPs)).
		Msg("[Admin] Loaded admin settings")
}

// isAuthenticated checks if the request has a valid admin session
func (a *Admin) isAuthenticated(r *http.Request) bool {
	// If no secret key is configured, deny all access
	if a.authManager == nil || !a.authManager.HasSecretKey() {
		return false
	}

	cookie, err := r.Cookie(adminCookieName)
	if err != nil {
		return false
	}

	return a.authManager.ValidateSession(cookie.Value)
}

// HandleAdminRequest routes /admin/* requests.
func (a *Admin) HandleAdminRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer) {
	route := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin"), "/")

	// Public routes (no authentication required)
	switch {
	case route == "login" && r.Method == http.MethodPost:
		a.handleLogin(w, r)
		return
	case route == "login":
		// Serve login page (GET)
		a.frontend.ServeAppStatic(w, r, "", serv)
		return
	case route == "logout" && r.Method == http.MethodPost:
		a.handleLogout(w, r)
		return
	case route == "auth/status" && r.Method == http.MethodGet:
		a.handleAuthStatus(w, r)
		return
	}

	// Protected routes - require authentication
	if !a.isAuthenticated(r) {
		// For page requests (no specific route), show login page
		if route == "" {
			a.frontend.ServeAppStatic(w, r, "", serv)
			return
		}
		// For API requests, return 401
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	switch {
	case route == "":
		a.frontend.ServeAppStatic(w, r, "", serv)
	case route == "leases" && r.Method == http.MethodGet:
		writeJSON(w, convertLeaseEntriesToRows(serv, a, true))
	case route == "leases/banned" && r.Method == http.MethodGet:
		writeJSON(w, serv.GetLeaseManager().GetBannedLeases())
	case route == "stats" && r.Method == http.MethodGet:
		writeJSON(w, map[string]interface{}{
			"leases_count": len(serv.GetAllLeaseEntries()),
			"uptime":       "TODO",
		})
	case route == "settings" && r.Method == http.MethodGet:
		a.handleGetSettings(w)
	case route == "settings/approval-mode":
		a.handleApprovalModeRequest(w, r, serv)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/ban"):
		a.handleLeaseBanRequest(w, r, serv, route)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/bps"):
		a.handleLeaseBPSRequest(w, r, serv, route)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/approve"):
		a.handleLeaseApproveRequest(w, r, serv, route)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/deny"):
		a.handleLeaseDenyRequest(w, r, serv, route)
	case strings.HasPrefix(route, "ips/") && strings.HasSuffix(route, "/ban"):
		a.handleIPBanRequest(w, r, serv, route)
	default:
		http.NotFound(w, r)
	}
}

// handleLogin handles POST /admin/login
func (a *Admin) handleLogin(w http.ResponseWriter, r *http.Request) {
	clientIP := manager.ExtractClientIP(r)

	// Check if IP is locked
	if a.authManager.IsIPLocked(clientIP) {
		remaining := a.authManager.GetLockRemainingSeconds(clientIP)
		w.WriteHeader(http.StatusTooManyRequests)
		writeJSON(w, map[string]interface{}{
			"success":           false,
			"error":             "Too many failed attempts. Please try again later.",
			"locked":            true,
			"remaining_seconds": remaining,
		})
		return
	}

	var req struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !a.authManager.ValidateKey(req.Key) {
		// Record failed attempt
		nowLocked := a.authManager.RecordFailedLogin(clientIP)
		log.Warn().Str("ip", clientIP).Bool("now_locked", nowLocked).Msg("[Admin] Failed login attempt")

		response := map[string]interface{}{
			"success": false,
			"error":   "Invalid key",
			"locked":  nowLocked,
		}
		if nowLocked {
			response["remaining_seconds"] = 60
		}
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, response)
		return
	}

	// Successful login
	a.authManager.ResetFailedLogin(clientIP)
	token := a.authManager.CreateSession()

	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400, // 24 hours
	})

	log.Info().Str("ip", clientIP).Msg("[Admin] Successful login")
	writeJSON(w, map[string]interface{}{
		"success": true,
	})
}

// handleLogout handles POST /admin/logout
func (a *Admin) handleLogout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(adminCookieName)
	if err == nil && cookie.Value != "" {
		a.authManager.DeleteSession(cookie.Value)
	}

	// Clear the cookie
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1, // Delete cookie
	})

	writeJSON(w, map[string]interface{}{
		"success": true,
	})
}

// handleAuthStatus handles GET /admin/auth/status
func (a *Admin) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	authenticated := a.isAuthenticated(r)

	// Check if secret key is configured
	authEnabled := a.authManager != nil && a.authManager.HasSecretKey()

	writeJSON(w, map[string]interface{}{
		"authenticated": authenticated,
		"auth_enabled":  authEnabled,
	})
}

func (a *Admin) handleLeaseBanRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, route string) {
	parts := strings.Split(route, "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}

	leaseID, ok := decodeLeaseID(parts[1])
	if !ok {
		http.Error(w, "Invalid lease ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPost:
		serv.GetLeaseManager().BanLease(leaseID)
		a.SaveSettings(serv)
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		serv.GetLeaseManager().UnbanLease(leaseID)
		a.SaveSettings(serv)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Admin) handleGetSettings(w http.ResponseWriter) {
	writeJSON(w, map[string]interface{}{
		"approval_mode":   a.approveManager.GetApprovalMode(),
		"approved_leases": a.approveManager.GetApprovedLeases(),
		"denied_leases":   a.approveManager.GetDeniedLeases(),
	})
}

func (a *Admin) handleApprovalModeRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]interface{}{
			"approval_mode": a.approveManager.GetApprovalMode(),
		})
	case http.MethodPost:
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		mode := manager.ApprovalMode(req.Mode)
		if mode != manager.ApprovalModeAuto && mode != manager.ApprovalModeManual {
			http.Error(w, "Invalid mode (must be 'auto' or 'manual')", http.StatusBadRequest)
			return
		}
		a.approveManager.SetApprovalMode(mode)
		a.SaveSettings(serv)
		log.Info().Str("mode", string(mode)).Msg("[Admin] Approval mode changed")
		writeJSON(w, map[string]interface{}{
			"approval_mode": mode,
		})
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Admin) handleLeaseApproveRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, route string) {
	parts := strings.Split(route, "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}

	leaseID, ok := decodeLeaseID(parts[1])
	if !ok {
		http.Error(w, "Invalid lease ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPost:
		a.approveManager.ApproveLease(leaseID)
		a.approveManager.UndenyLease(leaseID) // Remove from denied if exists
		a.SaveSettings(serv)
		log.Info().Str("lease_id", leaseID).Msg("[Admin] Lease approved")
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		a.approveManager.RevokeLease(leaseID)
		a.SaveSettings(serv)
		log.Info().Str("lease_id", leaseID).Msg("[Admin] Lease approval revoked")
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Admin) handleLeaseDenyRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, route string) {
	parts := strings.Split(route, "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}

	leaseID, ok := decodeLeaseID(parts[1])
	if !ok {
		http.Error(w, "Invalid lease ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPost:
		a.approveManager.DenyLease(leaseID)
		a.SaveSettings(serv)
		log.Info().Str("lease_id", leaseID).Msg("[Admin] Lease denied")
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		a.approveManager.UndenyLease(leaseID)
		a.SaveSettings(serv)
		log.Info().Str("lease_id", leaseID).Msg("[Admin] Lease denial removed")
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Admin) handleLeaseBPSRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, route string) {
	parts := strings.Split(route, "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}

	leaseID, ok := decodeLeaseID(parts[1])
	if !ok {
		http.Error(w, "Invalid lease ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPost:
		var req struct {
			BPS int64 `json:"bps"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		if a.bpsManager == nil {
			http.Error(w, "BPS manager not initialized", http.StatusInternalServerError)
			return
		}
		oldBPS := a.bpsManager.GetBPSLimit(leaseID)
		a.bpsManager.SetBPSLimit(leaseID, req.BPS)
		log.Info().
			Str("lease_id", leaseID).
			Int64("old_bps", oldBPS).
			Int64("new_bps", req.BPS).
			Msg("[Admin] BPS limit updated")
		a.SaveSettings(serv)
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		if a.bpsManager == nil {
			http.Error(w, "BPS manager not initialized", http.StatusInternalServerError)
			return
		}
		oldBPS := a.bpsManager.GetBPSLimit(leaseID)
		a.bpsManager.SetBPSLimit(leaseID, 0)
		log.Info().
			Str("lease_id", leaseID).
			Int64("old_bps", oldBPS).
			Msg("[Admin] BPS limit removed (now unlimited)")
		a.SaveSettings(serv)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Admin) handleIPBanRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, route string) {
	// Route format: ips/{ip}/ban
	parts := strings.Split(route, "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}

	ip := parts[1]
	if ip == "" {
		http.Error(w, "Invalid IP address", http.StatusBadRequest)
		return
	}

	if a.ipManager == nil {
		http.Error(w, "IP manager not initialized", http.StatusInternalServerError)
		return
	}

	switch r.Method {
	case http.MethodPost:
		a.ipManager.BanIP(ip)
		a.SaveSettings(serv)
		log.Info().Str("ip", ip).Msg("[Admin] IP banned")
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		a.ipManager.UnbanIP(ip)
		a.SaveSettings(serv)
		log.Info().Str("ip", ip).Msg("[Admin] IP unbanned")
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}
