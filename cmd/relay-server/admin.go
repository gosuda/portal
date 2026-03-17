package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

const cookieName = "portal_admin"
const adminSettingsPath = "admin_settings.json"

type adminAuth struct {
	sessions  map[string]time.Time
	secretKey string
	mu        sync.RWMutex
}

func newAdminAuth(secretKey string) *adminAuth {
	secretKey = strings.TrimSpace(secretKey)
	if secretKey == "" {
		generated, err := utils.RandomHex(16)
		if err != nil {
			log.Fatal().Err(err).Msg("generate admin secret key")
		}
		secretKey = generated
		log.Warn().
			Str("component", "relay-server-admin").
			Str("admin_secret_key", secretKey).
			Msg("generated random admin secret key because ADMIN_SECRET_KEY was empty")
	}

	return &adminAuth{
		secretKey: secretKey,
		sessions:  make(map[string]time.Time),
	}
}

func (a *adminAuth) AuthEnabled() bool {
	return a != nil && a.secretKey != ""
}

func (a *adminAuth) ValidateKey(key string) bool {
	if !a.AuthEnabled() {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a.secretKey), []byte(key)) == 1
}

func (a *adminAuth) CreateSession() (string, error) {
	token, err := utils.RandomHex(32)
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions[token] = time.Now().Add(24 * time.Hour)
	a.cleanupExpiredSessionsLocked()
	return token, nil
}

func (a *adminAuth) ValidateSession(token string) bool {
	if token == "" {
		return false
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	expiry, ok := a.sessions[token]
	return ok && time.Now().Before(expiry)
}

func (a *adminAuth) DeleteSession(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, token)
}

func (a *adminAuth) cleanupExpiredSessionsLocked() {
	now := time.Now()
	for token, expiry := range a.sessions {
		if now.After(expiry) {
			delete(a.sessions, token)
		}
	}
}

func loadAdminState(runtime *policy.Runtime) error {
	root, name, err := openSettingsRoot(adminSettingsPath)
	if err != nil {
		return err
	}
	defer root.Close()

	data, err := root.ReadFile(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	var payload persistedAdminState
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	return payload.apply(runtime)
}

func (f *Frontend) serveAdmin(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(strings.TrimSpace(r.URL.Path), "/")
	if path == "" {
		path = types.PathRoot
	}

	switch path {
	case types.PathAdmin:
		if r.Method == http.MethodGet {
			f.ServeAppStatic(w, r, "")
			return
		}
		http.NotFound(w, r)
		return
	case types.PathAdminLogin:
		if r.Method == http.MethodGet {
			f.ServeAppStatic(w, r, "")
			return
		}
		if r.Method == http.MethodPost {
			f.handleLogin(w, r)
			return
		}
		utils.WriteAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	case types.PathAdminLogout:
		if r.Method != http.MethodPost {
			utils.WriteAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
			return
		}
		if cookie, err := r.Cookie(cookieName); err == nil && cookie.Value != "" {
			f.auth.DeleteSession(cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    "",
			Path:     types.PathAdmin,
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   -1,
		})
		utils.WriteAPIOK(w, http.StatusOK)
		return
	case types.PathAdminAuthStatus:
		if r.Method != http.MethodGet {
			utils.WriteAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
			return
		}
		utils.WriteAPIData(w, http.StatusOK, types.AdminAuthStatusResponse{
			Authenticated: f.isAuthenticated(r),
			AuthEnabled:   f.auth.AuthEnabled(),
		})
		return
	}

	if !f.isAuthenticated(r) {
		utils.WriteAPIError(w, http.StatusUnauthorized, types.APIErrorCodeUnauthorized, "unauthorized")
		return
	}

	runtime := f.server.PolicyRuntime()
	methodNotAllowed := func() {
		utils.WriteAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
	}
	writeOK := func() {
		saveAdminState(runtime)
		utils.WriteAPIOK(w, http.StatusOK)
	}

	switch path {
	case types.PathAdminSnapshot:
		if r.Method != http.MethodGet {
			methodNotAllowed()
			return
		}
		utils.WriteAPIData(w, http.StatusOK, types.AdminSnapshotResponse{
			ApprovalMode: string(runtime.Approver().Mode()),
			Leases:       f.adminLeaseSnapshots(),
		})
	case types.PathAdminApproval:
		if r.Method != http.MethodPost {
			methodNotAllowed()
			return
		}
		var req types.AdminApprovalModeRequest
		if err := utils.DecodeJSONBody(w, r, &req, 1<<16); err != nil {
			utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "invalid request body")
			return
		}
		if err := runtime.Approver().SetMode(policy.Mode(strings.TrimSpace(req.Mode))); err != nil {
			utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidMode, "invalid mode (must be 'auto' or 'manual')")
			return
		}
		saveAdminState(runtime)
		utils.WriteAPIData(w, http.StatusOK, types.AdminApprovalModeResponse{
			ApprovalMode: string(runtime.Approver().Mode()),
		})
	default:
		switch {
		case strings.HasPrefix(path, types.PathAdminLeasesPrefix):
			rest := strings.TrimPrefix(path, types.PathAdminLeasesPrefix)
			parts := strings.Split(rest, "/")
			if len(parts) != 2 {
				http.NotFound(w, r)
				return
			}

			leaseID, err := utils.DecodeBase64URLString(parts[0])
			if err != nil {
				utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidLeaseID, "invalid lease ID")
				return
			}

			switch parts[1] {
			case "ban":
				switch r.Method {
				case http.MethodPost:
					runtime.BanLease(leaseID)
				case http.MethodDelete:
					runtime.UnbanLease(leaseID)
				default:
					methodNotAllowed()
					return
				}
				writeOK()
			case "bps":
				switch r.Method {
				case http.MethodPost:
					var req types.AdminBPSRequest
					if err := utils.DecodeJSONBody(w, r, &req, 1<<16); err != nil {
						utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "invalid request body")
						return
					}
					if req.BPS <= 0 {
						utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "bps must be greater than zero")
						return
					}
					runtime.BPSManager().SetLeaseBPS(leaseID, req.BPS)
				case http.MethodDelete:
					runtime.BPSManager().DeleteLeaseBPS(leaseID)
				default:
					methodNotAllowed()
					return
				}
				writeOK()
			case "approve":
				approver := runtime.Approver()
				switch r.Method {
				case http.MethodPost:
					approver.Approve(leaseID)
					approver.Undeny(leaseID)
				case http.MethodDelete:
					approver.Revoke(leaseID)
				default:
					methodNotAllowed()
					return
				}
				writeOK()
			case "deny":
				approver := runtime.Approver()
				switch r.Method {
				case http.MethodPost:
					approver.Deny(leaseID)
				case http.MethodDelete:
					approver.Undeny(leaseID)
				default:
					methodNotAllowed()
					return
				}
				writeOK()
			default:
				http.NotFound(w, r)
			}
		case strings.HasPrefix(path, types.PathAdminIPsPrefix):
			if !strings.HasSuffix(path, "/ban") {
				http.NotFound(w, r)
				return
			}

			rawIP := strings.TrimSuffix(strings.TrimPrefix(path, types.PathAdminIPsPrefix), "/ban")
			rawIP = strings.Trim(rawIP, "/")
			if net.ParseIP(rawIP) == nil {
				utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidIP, "invalid IP address")
				return
			}

			filter := runtime.IPFilter()
			switch r.Method {
			case http.MethodPost:
				filter.BanIP(rawIP)
			case http.MethodDelete:
				filter.UnbanIP(rawIP)
			default:
				methodNotAllowed()
				return
			}
			writeOK()
		default:
			http.NotFound(w, r)
		}
	}
}

func (f *Frontend) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.WriteAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}
	if !f.auth.AuthEnabled() {
		utils.WriteAPIError(w, http.StatusServiceUnavailable, types.APIErrorCodeAuthDisabled, "admin authentication is not configured")
		return
	}

	var req types.AdminLoginRequest
	if err := utils.DecodeJSONBody(w, r, &req, 1<<16); err != nil {
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "invalid request body")
		return
	}
	if !f.auth.ValidateKey(req.Key) {
		utils.WriteAPIError(w, http.StatusUnauthorized, types.APIErrorCodeInvalidKey, "Invalid key")
		return
	}
	token, err := f.auth.CreateSession()
	if err != nil {
		utils.WriteAPIError(w, http.StatusInternalServerError, types.APIErrorCodeSessionCreateFailed, "failed to create admin session")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     types.PathAdmin,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400,
	})
	utils.WriteAPIData(w, http.StatusOK, types.AdminLoginResponse{Success: true})
}

func (f *Frontend) isAuthenticated(r *http.Request) bool {
	if !f.auth.AuthEnabled() {
		return false
	}
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return f.auth.ValidateSession(cookie.Value)
}

func saveAdminState(runtime *policy.Runtime) {
	payload := persistedStateFromRuntime(runtime)
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return
	}

	root, name, err := openSettingsRoot(adminSettingsPath)
	if err != nil {
		return
	}
	defer root.Close()
	_ = root.WriteFile(name, data, 0o600)
}

type persistedAdminState struct {
	ApprovalMode   string           `json:"approval_mode"`
	ApprovedLeases []string         `json:"approved_leases,omitempty"`
	DeniedLeases   []string         `json:"denied_leases,omitempty"`
	BannedLeases   []string         `json:"banned_leases,omitempty"`
	BannedIPs      []string         `json:"banned_ips,omitempty"`
	LeaseBPS       map[string]int64 `json:"lease_bps,omitempty"`
}

func persistedStateFromRuntime(runtime *policy.Runtime) persistedAdminState {
	approver := runtime.Approver()
	return persistedAdminState{
		ApprovalMode:   string(approver.Mode()),
		ApprovedLeases: approver.ApprovedLeases(),
		DeniedLeases:   approver.DeniedLeases(),
		BannedLeases:   runtime.BannedLeases(),
		BannedIPs:      runtime.IPFilter().BannedIPs(),
		LeaseBPS:       runtime.BPSManager().LeaseBPSLimits(),
	}
}

func (s persistedAdminState) apply(runtime *policy.Runtime) error {
	if runtime == nil {
		return nil
	}
	if mode := strings.TrimSpace(s.ApprovalMode); mode != "" {
		if err := runtime.Approver().SetMode(policy.Mode(mode)); err != nil {
			return err
		}
	}
	runtime.Approver().SetDecisions(s.ApprovedLeases, s.DeniedLeases)
	runtime.SetBannedLeases(s.BannedLeases)
	runtime.IPFilter().SetBannedIPs(s.BannedIPs)
	runtime.BPSManager().SetLeaseBPSLimits(s.LeaseBPS)
	return nil
}

func openSettingsRoot(path string) (*os.Root, string, error) {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if dir == "" {
		dir = "."
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", err
	}
	return root, name, nil
}
