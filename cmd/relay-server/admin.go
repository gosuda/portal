package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/utils"
)

const adminCookieName = "portal_admin"

// Admin manages approval state and persistence for relay-server.
type Admin struct {
	startTime    time.Time
	settingsPath string
	settingsMu   sync.Mutex

	approveManager   *manager.ApproveManager
	bpsManager       *manager.BPSManager
	connLimitManager *manager.ConnLimitManager
	ipManager        *manager.IPManager
	authManager      *manager.AuthManager

	frontend *Frontend
}

func NewAdmin(defaultLeaseBPS int64, defaultMaxConns int64, frontend *Frontend, authManager *manager.AuthManager) *Admin {
	bpsManager := manager.NewBPSManager()
	if defaultLeaseBPS > 0 {
		bpsManager.SetDefaultBPS(defaultLeaseBPS)
	}
	connLimitManager := manager.NewConnLimitManager()
	if defaultMaxConns > 0 {
		connLimitManager.SetDefaultLimit(defaultMaxConns)
	}
	return &Admin{
		startTime:        time.Now(),
		settingsPath:     "admin_settings.json",
		approveManager:   manager.NewApproveManager(),
		bpsManager:       bpsManager,
		connLimitManager: connLimitManager,
		ipManager:        manager.NewIPManager(),
		authManager:      authManager,
		frontend:         frontend,
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

// GetConnLimitManager exposes the connection limit manager.
func (a *Admin) GetConnLimitManager() *manager.ConnLimitManager {
	return a.connLimitManager
}

// GetIPManager exposes the IP manager.
func (a *Admin) GetIPManager() *manager.IPManager {
	return a.ipManager
}

// adminSettings stores persistent admin configuration
type adminSettings struct {
	BannedLeases   []string             `json:"banned_leases"`
	BPSLimits      map[string]int64     `json:"bps_limits"`
	ConnLimits     map[string]int64     `json:"conn_limits,omitempty"`
	ApprovalMode   manager.ApprovalMode `json:"approval_mode"`
	ApprovedLeases []string             `json:"approved_leases,omitempty"`
	DeniedLeases   []string             `json:"denied_leases,omitempty"`
	BannedIPs      []string             `json:"banned_ips,omitempty"`
}

func (a *Admin) SaveSettings(lm *portal.LeaseManager) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()

	banned := lm.GetBannedLeases()

	bpsLimits := map[string]int64{}
	if a.bpsManager != nil {
		bpsLimits = a.bpsManager.GetAllBPSLimits()
	}

	connLimits := map[string]int64{}
	if a.connLimitManager != nil {
		connLimits = a.connLimitManager.GetAllLimits()
	}

	var bannedIPs []string
	if a.ipManager != nil {
		bannedIPs = a.ipManager.GetBannedIPs()
	}

	settings := adminSettings{
		BannedLeases:   banned,
		BPSLimits:      bpsLimits,
		ConnLimits:     connLimits,
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

func (a *Admin) LoadSettings(lm *portal.LeaseManager) {
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

	for _, leaseID := range settings.BannedLeases {
		lm.BanLease(leaseID)
	}

	for leaseID, bps := range settings.BPSLimits {
		if a.bpsManager != nil {
			a.bpsManager.SetBPSLimit(leaseID, bps)
		}
	}

	for leaseID, limit := range settings.ConnLimits {
		if a.connLimitManager != nil {
			a.connLimitManager.SetLimit(leaseID, limit)
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
		Int("conn_limits_count", len(settings.ConnLimits)).
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
func (a *Admin) HandleAdminRequest(w http.ResponseWriter, r *http.Request, lm *portal.LeaseManager) {
	route := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin"), "/")

	// Public routes (no authentication required)
	switch {
	case route == "login" && r.Method == http.MethodPost:
		a.handleLogin(w, r)
		return
	case route == "login":
		// Serve login page (GET)
		a.frontend.ServeAppStatic(w, r, "", lm)
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
			a.frontend.ServeAppStatic(w, r, "", lm)
			return
		}
		// For API requests, return 401
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	switch {
	case route == "":
		a.frontend.ServeAppStatic(w, r, "", lm)
	case route == "leases" && r.Method == http.MethodGet:
		writeJSON(w, a.convertLeaseEntriesToAdminRows(lm))
	case route == "leases/banned" && r.Method == http.MethodGet:
		writeJSON(w, lm.GetBannedLeases())
	case route == "stats" && r.Method == http.MethodGet:
		uptime := time.Since(a.startTime)
		writeJSON(w, map[string]interface{}{
			"leases_count":   len(lm.GetAllLeaseEntries()),
			"uptime":         uptime.Truncate(time.Second).String(),
			"uptime_seconds": int64(uptime.Seconds()),
		})
	case route == "settings" && r.Method == http.MethodGet:
		a.handleGetSettings(w)
	case route == "settings/approval-mode":
		a.handleApprovalModeRequest(w, r, lm)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/ban"):
		a.handleLeaseBanRequest(w, r, lm, route)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/bps"):
		a.handleLeaseBPSRequest(w, r, lm, route)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/max-conns"):
		a.handleLeaseConnLimitRequest(w, r, lm, route)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/approve"):
		a.handleLeaseApproveRequest(w, r, lm, route)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/deny"):
		a.handleLeaseDenyRequest(w, r, lm, route)
	case strings.HasPrefix(route, "ips/") && strings.HasSuffix(route, "/ban"):
		a.handleIPBanRequest(w, r, lm, route)
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

	secureCookie := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   secureCookie,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
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
	secureCookie := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		Secure:   secureCookie,
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

func (a *Admin) handleLeaseBanRequest(w http.ResponseWriter, r *http.Request, lm *portal.LeaseManager, route string) {
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
		lm.BanLease(leaseID)
		a.SaveSettings(lm)
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		lm.UnbanLease(leaseID)
		a.SaveSettings(lm)
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

func (a *Admin) handleApprovalModeRequest(w http.ResponseWriter, r *http.Request, lm *portal.LeaseManager) {
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
		a.SaveSettings(lm)
		log.Info().Str("mode", string(mode)).Msg("[Admin] Approval mode changed")
		writeJSON(w, map[string]interface{}{
			"approval_mode": mode,
		})
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Admin) handleLeaseApproveRequest(w http.ResponseWriter, r *http.Request, lm *portal.LeaseManager, route string) {
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

		a.SaveSettings(lm)
		log.Info().Str("lease_id", leaseID).Msg("[Admin] Lease approved")
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		a.approveManager.RevokeLease(leaseID)
		a.SaveSettings(lm)
		log.Info().Str("lease_id", leaseID).Msg("[Admin] Lease approval revoked")
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Admin) handleLeaseDenyRequest(w http.ResponseWriter, r *http.Request, lm *portal.LeaseManager, route string) {
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
		a.SaveSettings(lm)
		log.Info().Str("lease_id", leaseID).Msg("[Admin] Lease denied")
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		a.approveManager.UndenyLease(leaseID)
		a.SaveSettings(lm)
		log.Info().Str("lease_id", leaseID).Msg("[Admin] Lease denial removed")
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Admin) handleLeaseBPSRequest(w http.ResponseWriter, r *http.Request, lm *portal.LeaseManager, route string) {
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
		a.SaveSettings(lm)
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
		a.SaveSettings(lm)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Admin) handleLeaseConnLimitRequest(w http.ResponseWriter, r *http.Request, lm *portal.LeaseManager, route string) {
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

	if a.connLimitManager == nil {
		http.Error(w, "Connection limit manager not initialized", http.StatusInternalServerError)
		return
	}

	switch r.Method {
	case http.MethodPost:
		var req struct {
			MaxConns int64 `json:"max_conns"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		oldLimit := a.connLimitManager.GetLimit(leaseID)
		a.connLimitManager.SetLimit(leaseID, req.MaxConns)
		log.Info().
			Str("lease_id", leaseID).
			Int64("old_limit", oldLimit).
			Int64("new_limit", req.MaxConns).
			Msg("[Admin] Connection limit updated")
		a.SaveSettings(lm)
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		oldLimit := a.connLimitManager.GetLimit(leaseID)
		a.connLimitManager.SetLimit(leaseID, 0)
		log.Info().
			Str("lease_id", leaseID).
			Int64("old_limit", oldLimit).
			Msg("[Admin] Connection limit removed (now using default)")
		a.SaveSettings(lm)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (a *Admin) handleIPBanRequest(w http.ResponseWriter, r *http.Request, lm *portal.LeaseManager, route string) {
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
		a.SaveSettings(lm)
		log.Info().Str("ip", ip).Msg("[Admin] IP banned")
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		a.ipManager.UnbanIP(ip)
		a.SaveSettings(lm)
		log.Info().Str("ip", ip).Msg("[Admin] IP unbanned")
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// convertLeaseEntriesToAdminRows converts LeaseEntry data to leaseRow format for admin API
func (a *Admin) convertLeaseEntriesToAdminRows(lm *portal.LeaseManager) []leaseRow {
	leaseEntries := lm.GetAllLeaseEntries()
	rows := []leaseRow{}
	now := time.Now()

	for _, leaseEntry := range leaseEntries {
		if now.After(leaseEntry.Expires) {
			continue
		}

		lease := leaseEntry.Lease
		leaseID := lease.ID

		ttl := time.Until(leaseEntry.Expires)
		ttlStr := ""
		if ttl > 0 {
			if ttl > time.Hour {
				ttlStr = fmt.Sprintf("%.0fh", ttl.Hours())
			} else if ttl > time.Minute {
				ttlStr = fmt.Sprintf("%.0fm", ttl.Minutes())
			} else {
				ttlStr = fmt.Sprintf("%.0fs", ttl.Seconds())
			}
		}

		since := now.Sub(leaseEntry.LastSeen)
		if since < 0 {
			since = 0
		}
		lastSeenStr := formatDuration(since)
		lastSeenISO := leaseEntry.LastSeen.UTC().Format(time.RFC3339)
		firstSeenISO := leaseEntry.FirstSeen.UTC().Format(time.RFC3339)

		// Funnel leases are considered connected if not expired and recently seen
		connected := since < 60*time.Second

		name := lease.Name
		if name == "" {
			name = "(unnamed)"
		}

		kind := "funnel"

		dnsLabel := leaseID
		if len(dnsLabel) > 8 {
			dnsLabel = dnsLabel[:8] + "..."
		}

		var link string
		if leaseEntry.ReverseToken != "" && flagFunnelDomain != "" {
			if flagFunnelPort == 443 {
				link = fmt.Sprintf("https://%s.%s/", lease.Name, flagFunnelDomain)
			} else {
				link = fmt.Sprintf("https://%s.%s:%d/", lease.Name, flagFunnelDomain, flagFunnelPort)
			}
		} else {
			base := flagPortalAppURL
			if base == "" {
				base = flagPortalURL
			}
			link = fmt.Sprintf("//%s.%s/", lease.Name, utils.StripWildCard(utils.StripScheme(base)))
		}

		var bps int64
		if a.bpsManager != nil {
			bps = a.bpsManager.GetBPSLimit(leaseID)
		}

		var maxConns, activeConns int64
		if a.connLimitManager != nil {
			maxConns = a.connLimitManager.GetLimit(leaseID)
			activeConns = a.connLimitManager.ActiveCount(leaseID)
		}

		// Get IP info for this lease
		var ip string
		var isIPBanned bool
		if a.ipManager != nil {
			ip = a.ipManager.GetLeaseIP(leaseID)
			if ip != "" {
				isIPBanned = a.ipManager.IsIPBanned(ip)
			}
		}

		rows = append(rows, leaseRow{
			Peer:         leaseID,
			Name:         name,
			Kind:         kind,
			Connected:    connected,
			DNS:          dnsLabel,
			LastSeen:     lastSeenStr,
			LastSeenISO:  lastSeenISO,
			FirstSeenISO: firstSeenISO,
			TTL:          ttlStr,
			Link:         link,
			StaleRed:     !connected && since >= 15*time.Second,
			Hide:         leaseEntry.ParsedMetadata != nil && leaseEntry.ParsedMetadata.Hide,
			Metadata:     lease.Metadata,
			BPS:          bps,
			MaxConns:     maxConns,
			ActiveConns:  activeConns,
			IsApproved:   a.approveManager.GetApprovalMode() == manager.ApprovalModeAuto || a.approveManager.IsLeaseApproved(leaseID),
			IsDenied:     a.approveManager.IsLeaseDenied(leaseID),
			IP:           ip,
			IsIPBanned:   isIPBanned,
		})
	}

	return rows
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error().Err(err).Msg("[HTTP] Failed to encode response")
	}
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
