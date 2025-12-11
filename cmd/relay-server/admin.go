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
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/ban"):
		handleLeaseBanRequest(w, r, serv, route)
	case strings.HasPrefix(route, "leases/") && strings.HasSuffix(route, "/bps"):
		handleLeaseBPSRequest(w, r, serv, route)
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

		rows = append(rows, leaseRow{
			Peer:        identityID,
			Name:        name,
			Kind:        kind,
			Connected:   connected,
			DNS:         dnsLabel,
			LastSeen:    lastSeenStr,
			LastSeenISO: lastSeenISO,
			TTL:         ttlStr,
			Link:        link,
			StaleRed:    !connected && since >= 15*time.Second,
			Metadata:    lease.Metadata,
			BPS:         bps,
		})
	}

	return rows
}

// AdminSettings stores persistent admin configuration
type AdminSettings struct {
	BannedLeases []string         `json:"banned_leases"`
	BPSLimits    map[string]int64 `json:"bps_limits"`
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

	settings := AdminSettings{
		BannedLeases: banned,
		BPSLimits:    bpsLimits,
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

func loadAdminSettings(serv *portal.RelayServer, bpsManager *BPSManager) {
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

	log.Info().
		Int("banned_count", len(settings.BannedLeases)).
		Int("bps_limits_count", len(settings.BPSLimits)).
		Msg("[Admin] Loaded admin settings")
}
