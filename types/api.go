package types

import "encoding/json"

// API path constants for Portal relay server.

// SDK API paths for lease registration and tunnel connections.
const (
	PathSDKPrefix     = "/sdk/"
	PathSDKRegister   = "/sdk/register"
	PathSDKUnregister = "/sdk/unregister"
	PathSDKRenew      = "/sdk/renew"
	PathSDKDomain     = "/sdk/domain"
	PathSDKConnect    = "/sdk/connect"

	// Admin API paths.
	PathAdminPrefix       = "/admin"
	PathAdminLogin        = "/admin/login"
	PathAdminLogout       = "/admin/logout"
	PathAdminAuthStatus   = "/admin/auth/status"
	PathAdminLeases       = "/admin/leases"
	PathAdminLeasesBanned = "/admin/leases/banned"
	PathAdminStats        = "/admin/stats"
	PathAdminSettings     = "/admin/settings"
	PathAdminApprovalMode = "/admin/settings/approval-mode"

	// Keyless API paths.
	PathKeylessSign = "/v1/sign"

	// Health check path.
	PathHealthz = "/healthz"

	// Tunnel installer paths.
	PathTunnelScript = "/tunnel"
	PathTunnelBinary = "/tunnel/bin/"

	// App static assets path prefix.
	PathAppPrefix = "/app/"
)

// Client API types for /sdk/* endpoints.

// APIError is the normalized API error payload.
type APIError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	StatusCode int    `json:"-"` // HTTP status code, not serialized
}

// APIEnvelope is the canonical response wrapper for relay APIs.
type APIEnvelope struct {
	Data  any       `json:"data,omitempty"`
	Error *APIError `json:"error,omitempty"`
	OK    bool      `json:"ok"`
}

// APIRawEnvelope is the decoding-friendly envelope with raw data payload.
type APIRawEnvelope struct {
	Error *APIError       `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	OK    bool            `json:"ok"`
}

// RegisterRequest is the lease registration request.
type RegisterRequest struct {
	LeaseID      string   `json:"lease_id"`
	Name         string   `json:"name"`
	ReverseToken string   `json:"reverse_token"`
	Metadata     Metadata `json:"metadata"`
	TLS          bool     `json:"tls"`
}

// RegisterResponse is the lease registration response.
type RegisterResponse struct {
	Message   string `json:"message,omitempty"`
	LeaseID   string `json:"lease_id,omitempty"`
	PublicURL string `json:"public_url,omitempty"`
	Success   bool   `json:"success"`
}

// UnregisterRequest is the lease unregistration request.
type UnregisterRequest struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
}

// RenewRequest is the lease renewal request.
type RenewRequest struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
}

// APIResponse is a generic Client API response.
type APIResponse struct {
	Message string `json:"message,omitempty"`
	Success bool   `json:"success"`
}

// DomainResponse is the Client domain discovery response.
type DomainResponse struct {
	Message    string `json:"message,omitempty"`
	BaseDomain string `json:"base_domain,omitempty"`
	Success    bool   `json:"success"`
}

// Admin API types for /admin/* endpoints.

// AdminLoginRequest is the admin login request body.
type AdminLoginRequest struct {
	Key string `json:"key"`
}

// AdminLoginResponse is the admin login response.
type AdminLoginResponse struct {
	Error            string `json:"error,omitempty"`
	RemainingSeconds int    `json:"remaining_seconds,omitempty"`
	Success          bool   `json:"success"`
	Locked           bool   `json:"locked,omitempty"`
}

// AdminAuthStatusResponse is the admin auth status response.
type AdminAuthStatusResponse struct {
	Authenticated bool `json:"authenticated"`
	AuthEnabled   bool `json:"auth_enabled"`
}

// AdminSettingsResponse is the admin settings response.
type AdminSettingsResponse struct {
	ApprovalMode   string   `json:"approval_mode"`
	ApprovedLeases []string `json:"approved_leases"`
	DeniedLeases   []string `json:"denied_leases"`
}

// AdminApprovalModeRequest is the request to change approval mode.
type AdminApprovalModeRequest struct {
	Mode string `json:"mode"`
}

// AdminApprovalModeResponse is the approval mode response.
type AdminApprovalModeResponse struct {
	ApprovalMode string `json:"approval_mode"`
}

// AdminBPSRequest is the request to set BPS limit for a lease.
type AdminBPSRequest struct {
	BPS int64 `json:"bps"`
}

// AdminStatsResponse is the admin stats response.
type AdminStatsResponse struct {
	Uptime      string `json:"uptime"`
	LeasesCount int    `json:"leases_count"`
}
