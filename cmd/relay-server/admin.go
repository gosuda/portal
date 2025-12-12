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

	"gosuda.org/portal/portal"
	"gosuda.org/portal/utils"
)

func handleAdminRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer) {
	if !utils.IsLocalhost(r) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	route := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin"), "/")

	switch {
	case route == "":
		serveAppStatic(w, r, "", serv)
	case route == "leases" && r.Method == http.MethodGet:
		writeJSON(w, convertLeaseEntriesToAdminRows(serv))
	case route == "leases/banned" && r.Method == http.MethodGet:
		writeJSON(w, serv.GetLeaseManager().GetBannedLeases())
	case route == "stats" && r.Method == http.MethodGet:
		writeJSON(w, map[string]interface{}{
			"leases_count": len(serv.GetAllLeaseEntries()),
			"uptime":       "TODO",
		})
	case route == "settings" && r.Method == http.MethodGet:
		handleGetSettings(w)
	case route == "settings/approval-mode":
		handleApprovalModeRequest(w, r, serv)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/ban"):
		handleLeaseBanRequest(w, r, serv, route)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/bps"):
		handleLeaseBPSRequest(w, r, serv, route)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/approve"):
		handleLeaseApproveRequest(w, r, serv, route)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/deny"):
		handleLeaseDenyRequest(w, r, serv, route)
	case strings.HasPrefix(route, "ips/") && strings.HasSuffix(route, "/ban"):
		handleIPBanRequest(w, r, serv, route)
	default:
		http.NotFound(w, r)
	}
}

func handleLeaseBanRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, route string) {
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
		saveAdminSettings(serv, globalBPSManager)
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		serv.GetLeaseManager().UnbanLease(leaseID)
		saveAdminSettings(serv, globalBPSManager)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func handleGetSettings(w http.ResponseWriter) {
	writeJSON(w, map[string]interface{}{
		"approval_mode":   getApprovalMode(),
		"approved_leases": getApprovedLeases(),
		"denied_leases":   getDeniedLeases(),
	})
}

func handleApprovalModeRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]interface{}{
			"approval_mode": getApprovalMode(),
		})
	case http.MethodPost:
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
		mode := ApprovalMode(req.Mode)
		if mode != ApprovalModeAuto && mode != ApprovalModeManual {
			http.Error(w, "Invalid mode (must be 'auto' or 'manual')", http.StatusBadRequest)
			return
		}
		setApprovalMode(mode)
		saveAdminSettings(serv, globalBPSManager)
		log.Info().Str("mode", string(mode)).Msg("[Admin] Approval mode changed")
		writeJSON(w, map[string]interface{}{
			"approval_mode": mode,
		})
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func handleLeaseApproveRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, route string) {
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
		approveLease(leaseID)
		undenyLease(leaseID) // Remove from denied if exists
		saveAdminSettings(serv, globalBPSManager)
		log.Info().Str("lease_id", leaseID).Msg("[Admin] Lease approved")
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		revokeLease(leaseID)
		saveAdminSettings(serv, globalBPSManager)
		log.Info().Str("lease_id", leaseID).Msg("[Admin] Lease approval revoked")
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func handleLeaseDenyRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, route string) {
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
		denyLease(leaseID)
		saveAdminSettings(serv, globalBPSManager)
		log.Info().Str("lease_id", leaseID).Msg("[Admin] Lease denied")
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		undenyLease(leaseID)
		saveAdminSettings(serv, globalBPSManager)
		log.Info().Str("lease_id", leaseID).Msg("[Admin] Lease denial removed")
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func handleLeaseBPSRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, route string) {
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
		oldBPS := globalBPSManager.GetBPSLimit(leaseID)
		globalBPSManager.SetBPSLimit(leaseID, req.BPS)
		log.Info().
			Str("lease_id", leaseID).
			Int64("old_bps", oldBPS).
			Int64("new_bps", req.BPS).
			Msg("[Admin] BPS limit updated")
		saveAdminSettings(serv, globalBPSManager)
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		oldBPS := globalBPSManager.GetBPSLimit(leaseID)
		globalBPSManager.SetBPSLimit(leaseID, 0)
		log.Info().
			Str("lease_id", leaseID).
			Int64("old_bps", oldBPS).
			Msg("[Admin] BPS limit removed (now unlimited)")
		saveAdminSettings(serv, globalBPSManager)
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func handleIPBanRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, route string) {
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

	if globalIPManager == nil {
		http.Error(w, "IP manager not initialized", http.StatusInternalServerError)
		return
	}

	switch r.Method {
	case http.MethodPost:
		globalIPManager.BanIP(ip)
		saveAdminSettings(serv, globalBPSManager)
		log.Info().Str("ip", ip).Msg("[Admin] IP banned")
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		globalIPManager.UnbanIP(ip)
		saveAdminSettings(serv, globalBPSManager)
		log.Info().Str("ip", ip).Msg("[Admin] IP unbanned")
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
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

// convertLeaseEntriesToAdminRows converts LeaseEntry data to leaseRow format for admin API
func convertLeaseEntriesToAdminRows(serv *portal.RelayServer) []leaseRow {
	leaseEntries := serv.GetAllLeaseEntries()
	rows := []leaseRow{}
	now := time.Now()

	for _, leaseEntry := range leaseEntries {
		if now.After(leaseEntry.Expires) {
			continue
		}

		lease := leaseEntry.Lease
		identityID := string(lease.Identity.Id)

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
		lastSeenStr := func(d time.Duration) string {
			if d >= time.Hour {
				h := int(d / time.Hour)
				m := int((d % time.Hour) / time.Minute)
				if m > 0 {
					return fmt.Sprintf("%dh %dm", h, m)
				}
				return fmt.Sprintf("%dh", h)
			}
			if d >= time.Minute {
				m := int(d / time.Minute)
				s := int((d % time.Minute) / time.Second)
				if s > 0 {
					return fmt.Sprintf("%dm %ds", m, s)
				}
				return fmt.Sprintf("%dm", m)
			}
			return fmt.Sprintf("%ds", int(d/time.Second))
		}(since)
		lastSeenISO := leaseEntry.LastSeen.UTC().Format(time.RFC3339)
		firstSeenISO := leaseEntry.FirstSeen.UTC().Format(time.RFC3339)

		connected := serv.IsConnectionActive(leaseEntry.ConnectionID)

		name := lease.Name
		if name == "" {
			name = "(unnamed)"
		}

		kind := "client"
		if len(lease.Alpn) > 0 {
			kind = lease.Alpn[0]
		}

		dnsLabel := identityID
		if len(dnsLabel) > 8 {
			dnsLabel = dnsLabel[:8] + "..."
		}

		base := flagPortalAppURL
		if base == "" {
			base = flagPortalURL
		}
		link := fmt.Sprintf("//%s.%s/", lease.Name, utils.StripWildCard(utils.StripScheme(base)))

		bps := globalBPSManager.GetBPSLimit(identityID)

		// Get IP info for this lease
		var ip string
		var isIPBanned bool
		if globalIPManager != nil {
			ip = globalIPManager.GetLeaseIP(identityID)
			if ip != "" {
				isIPBanned = globalIPManager.IsIPBanned(ip)
			}
		}

		rows = append(rows, leaseRow{
			Peer:         identityID,
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
			IsApproved:   getApprovalMode() == ApprovalModeAuto || isLeaseApproved(identityID),
			IsDenied:     isLeaseDenied(identityID),
			IP:           ip,
			IsIPBanned:   isIPBanned,
		})
	}

	return rows
}

// ApprovalMode represents the approval mode for new connections
type ApprovalMode string

const (
	ApprovalModeAuto   ApprovalMode = "auto"
	ApprovalModeManual ApprovalMode = "manual"
)

// AdminSettings stores persistent admin configuration
type AdminSettings struct {
	BannedLeases   []string         `json:"banned_leases"`
	BPSLimits      map[string]int64 `json:"bps_limits"`
	ApprovalMode   ApprovalMode     `json:"approval_mode"`
	ApprovedLeases []string         `json:"approved_leases,omitempty"`
	DeniedLeases   []string         `json:"denied_leases,omitempty"`
	BannedIPs      []string         `json:"banned_ips,omitempty"`
}

// Global approval mode (default: auto)
var (
	globalApprovalMode   ApprovalMode = ApprovalModeAuto
	globalApprovedLeases              = make(map[string]struct{})
	globalDeniedLeases                = make(map[string]struct{})
	approvalMu           sync.RWMutex
)

func getApprovalMode() ApprovalMode {
	approvalMu.RLock()
	defer approvalMu.RUnlock()
	return globalApprovalMode
}

func setApprovalMode(mode ApprovalMode) {
	approvalMu.Lock()
	defer approvalMu.Unlock()
	globalApprovalMode = mode
}

func isLeaseApproved(leaseID string) bool {
	approvalMu.RLock()
	defer approvalMu.RUnlock()
	_, ok := globalApprovedLeases[leaseID]
	return ok
}

func approveLease(leaseID string) {
	approvalMu.Lock()
	defer approvalMu.Unlock()
	globalApprovedLeases[leaseID] = struct{}{}
}

func revokeLease(leaseID string) {
	approvalMu.Lock()
	defer approvalMu.Unlock()
	delete(globalApprovedLeases, leaseID)
}

func getApprovedLeases() []string {
	approvalMu.RLock()
	defer approvalMu.RUnlock()
	result := make([]string, 0, len(globalApprovedLeases))
	for id := range globalApprovedLeases {
		result = append(result, id)
	}
	return result
}

func isLeaseDenied(leaseID string) bool {
	approvalMu.RLock()
	defer approvalMu.RUnlock()
	_, ok := globalDeniedLeases[leaseID]
	return ok
}

func denyLease(leaseID string) {
	approvalMu.Lock()
	defer approvalMu.Unlock()
	globalDeniedLeases[leaseID] = struct{}{}
	// Remove from approved if exists
	delete(globalApprovedLeases, leaseID)
}

func undenyLease(leaseID string) {
	approvalMu.Lock()
	defer approvalMu.Unlock()
	delete(globalDeniedLeases, leaseID)
}

func getDeniedLeases() []string {
	approvalMu.RLock()
	defer approvalMu.RUnlock()
	result := make([]string, 0, len(globalDeniedLeases))
	for id := range globalDeniedLeases {
		result = append(result, id)
	}
	return result
}

var (
	adminSettingsPath = "admin_settings.json"
	adminSettingsMu   sync.Mutex
)

// SetAdminSettingsPath sets the path for admin settings file
func SetAdminSettingsPath(path string) {
	adminSettingsMu.Lock()
	defer adminSettingsMu.Unlock()
	adminSettingsPath = path
}

func saveAdminSettings(serv *portal.RelayServer, bpsManager *BPSManager) {
	adminSettingsMu.Lock()
	defer adminSettingsMu.Unlock()

	lm := serv.GetLeaseManager()

	bannedBytes := lm.GetBannedLeases()
	banned := make([]string, len(bannedBytes))
	for i, b := range bannedBytes {
		banned[i] = string(b)
	}

	bpsLimits := bpsManager.GetAllBPSLimits()

	var bannedIPs []string
	if globalIPManager != nil {
		bannedIPs = globalIPManager.GetBannedIPs()
	}

	settings := AdminSettings{
		BannedLeases:   banned,
		BPSLimits:      bpsLimits,
		ApprovalMode:   getApprovalMode(),
		ApprovedLeases: getApprovedLeases(),
		DeniedLeases:   getDeniedLeases(),
		BannedIPs:      bannedIPs,
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("[Admin] Failed to marshal admin settings")
		return
	}

	dir := filepath.Dir(adminSettingsPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Error().Err(err).Msg("[Admin] Failed to create settings directory")
			return
		}
	}

	if err := os.WriteFile(adminSettingsPath, data, 0644); err != nil {
		log.Error().Err(err).Msg("[Admin] Failed to save admin settings")
		return
	}

	log.Debug().Str("path", adminSettingsPath).Msg("[Admin] Saved admin settings")
}

func loadAdminSettings(serv *portal.RelayServer, bpsManager *BPSManager, ipManager *IPManager) {
	adminSettingsMu.Lock()
	defer adminSettingsMu.Unlock()

	data, err := os.ReadFile(adminSettingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug().Msg("[Admin] No admin settings file found, starting fresh")
			return
		}
		log.Error().Err(err).Msg("[Admin] Failed to read admin settings")
		return
	}

	var settings AdminSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		log.Error().Err(err).Msg("[Admin] Failed to parse admin settings")
		return
	}

	lm := serv.GetLeaseManager()

	for _, leaseID := range settings.BannedLeases {
		lm.BanLease(leaseID)
	}

	for leaseID, bps := range settings.BPSLimits {
		bpsManager.SetBPSLimit(leaseID, bps)
	}

	// Load approval mode
	if settings.ApprovalMode != "" {
		setApprovalMode(settings.ApprovalMode)
	}

	// Load approved leases
	for _, leaseID := range settings.ApprovedLeases {
		approveLease(leaseID)
	}

	// Load denied leases
	for _, leaseID := range settings.DeniedLeases {
		denyLease(leaseID)
	}

	// Load banned IPs
	if ipManager != nil && len(settings.BannedIPs) > 0 {
		ipManager.SetBannedIPs(settings.BannedIPs)
	}

	log.Info().
		Int("banned_count", len(settings.BannedLeases)).
		Int("bps_limits_count", len(settings.BPSLimits)).
		Str("approval_mode", string(getApprovalMode())).
		Int("approved_count", len(settings.ApprovedLeases)).
		Int("denied_count", len(settings.DeniedLeases)).
		Int("banned_ips_count", len(settings.BannedIPs)).
		Msg("[Admin] Loaded admin settings")
}
