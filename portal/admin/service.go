package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/policy"
)

const CookieName = "portal_admin"

// Service manages admin policy state and persistence.
type Service struct {
	approveManager *policy.Approver
	bpsManager     *policy.RateLimiter
	ipManager      *policy.IPFilter
	authManager    *policy.Authenticator
	settingsPath   string
	settingsMu     sync.Mutex
}

// settings stores persistent admin configuration.
type settings struct {
	BannedLeases   []string         `json:"banned_leases"`
	BPSLimits      map[string]int64 `json:"bps_limits"`
	ApprovalMode   policy.Mode      `json:"approval_mode"`
	ApprovedLeases []string         `json:"approved_leases,omitempty"`
	DeniedLeases   []string         `json:"denied_leases,omitempty"`
	BannedIPs      []string         `json:"banned_ips,omitempty"`
}

func NewService(defaultLeaseBPS int64, authManager *policy.Authenticator) *Service {
	bpsManager := policy.NewRateLimiter()
	if defaultLeaseBPS > 0 {
		bpsManager.SetDefaultBPS(defaultLeaseBPS)
	}

	return &Service{
		settingsPath:   "admin_settings.json",
		approveManager: policy.NewApprover(),
		bpsManager:     bpsManager,
		ipManager:      policy.NewIPFilter(),
		authManager:    authManager,
	}
}

func (s *Service) isUnavailable() bool {
	return s == nil
}

func (s *Service) isUnavailableForServer(serv *portal.RelayServer) bool {
	return s == nil || serv == nil
}

func (s *Service) authUnavailable() bool {
	return s == nil || s.authManager == nil || !s.authManager.HasSecretKey()
}

func (s *Service) GetApproveManager() *policy.Approver {
	if s.isUnavailable() {
		return nil
	}
	return s.approveManager
}

func (s *Service) GetBPSManager() *policy.RateLimiter {
	if s.isUnavailable() {
		return nil
	}
	return s.bpsManager
}

func (s *Service) GetIPManager() *policy.IPFilter {
	if s.isUnavailable() {
		return nil
	}
	return s.ipManager
}

func (s *Service) GetAuthManager() *policy.Authenticator {
	if s.isUnavailable() {
		return nil
	}
	return s.authManager
}

func (s *Service) SetSettingsPath(path string) {
	if s.isUnavailable() {
		return
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	s.settingsPath = path
}

func (s *Service) SaveSettings(serv *portal.RelayServer) {
	if s.isUnavailableForServer(serv) {
		return
	}

	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()

	lm := serv.GetLeaseManager()
	banned := lm.GetBannedLeases()

	bpsLimits := map[string]int64{}
	if s.bpsManager != nil {
		bpsLimits = s.bpsManager.GetAllBPSLimits()
	}

	var bannedIPs []string
	if s.ipManager != nil {
		bannedIPs = s.ipManager.GetBannedIPs()
	}

	payload := settings{
		BannedLeases:   banned,
		BPSLimits:      bpsLimits,
		ApprovalMode:   s.approveManager.GetApprovalMode(),
		ApprovedLeases: s.approveManager.GetApprovedLeases(),
		DeniedLeases:   s.approveManager.GetDeniedLeases(),
		BannedIPs:      bannedIPs,
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		log.Error().Err(err).Msg("[Admin] Failed to marshal admin settings")
		return
	}

	dir := filepath.Dir(s.settingsPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Error().Err(err).Msg("[Admin] Failed to create settings directory")
			return
		}
	}

	if err := os.WriteFile(s.settingsPath, data, 0644); err != nil {
		log.Error().Err(err).Msg("[Admin] Failed to save admin settings")
		return
	}

	log.Debug().Str("path", s.settingsPath).Msg("[Admin] Saved admin settings")
}

func (s *Service) LoadSettings(serv *portal.RelayServer) {
	if s.isUnavailableForServer(serv) {
		return
	}

	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()

	data, err := os.ReadFile(s.settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug().Msg("[Admin] No admin settings file found, starting fresh")
			return
		}
		log.Error().Err(err).Msg("[Admin] Failed to read admin settings")
		return
	}

	var payload settings
	if err := json.Unmarshal(data, &payload); err != nil {
		log.Error().Err(err).Msg("[Admin] Failed to parse admin settings")
		return
	}

	lm := serv.GetLeaseManager()

	for _, leaseID := range payload.BannedLeases {
		lm.BanLease(leaseID)
	}

	for leaseID, bps := range payload.BPSLimits {
		if s.bpsManager != nil {
			s.bpsManager.SetBPSLimit(leaseID, bps)
		}
	}

	if payload.ApprovalMode != "" {
		s.approveManager.SetApprovalMode(payload.ApprovalMode)
	}

	for _, leaseID := range payload.ApprovedLeases {
		s.approveManager.ApproveLease(leaseID)
	}

	for _, leaseID := range payload.DeniedLeases {
		s.approveManager.DenyLease(leaseID)
	}

	if s.ipManager != nil && len(payload.BannedIPs) > 0 {
		s.ipManager.SetBannedIPs(payload.BannedIPs)
	}

	log.Info().
		Int("banned_count", len(payload.BannedLeases)).
		Int("bps_limits_count", len(payload.BPSLimits)).
		Str("approval_mode", string(s.approveManager.GetApprovalMode())).
		Int("approved_count", len(payload.ApprovedLeases)).
		Int("denied_count", len(payload.DeniedLeases)).
		Int("banned_ips_count", len(payload.BannedIPs)).
		Msg("[Admin] Loaded admin settings")
}

// IsAuthenticated checks if the request has a valid admin session.
func (s *Service) IsAuthenticated(r *http.Request) bool {
	if s.authUnavailable() {
		return false
	}

	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return false
	}

	return s.authManager.ValidateSession(cookie.Value)
}

func (s *Service) AuthEnabled() bool {
	return !s.authUnavailable()
}
