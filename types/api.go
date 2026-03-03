package types

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

// RegisterRequest is the lease registration request.
type RegisterRequest struct {
	LeaseID      string   `json:"lease_id"`
	Name         string   `json:"name"`
	Metadata     Metadata `json:"metadata"`
	TLS          bool     `json:"tls"`
	ReverseToken string   `json:"reverse_token"`
}

// RegisterResponse is the lease registration response.
type RegisterResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message,omitempty"`
	LeaseID   string `json:"lease_id,omitempty"`
	PublicURL string `json:"public_url,omitempty"`
}

// UnregisterRequest is the lease unregistration request.
type UnregisterRequest struct {
	LeaseID string `json:"lease_id"`
}

// RenewRequest is the lease renewal request.
type RenewRequest struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
}

// APIResponse is a generic Client API response.
type APIResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// DomainResponse is the Client domain discovery response.
type DomainResponse struct {
	Success    bool   `json:"success"`
	Message    string `json:"message,omitempty"`
	BaseDomain string `json:"base_domain,omitempty"`
}

// Admin API types for /admin/* endpoints.

// AdminLoginRequest is the admin login request body.
type AdminLoginRequest struct {
	Key string `json:"key"`
}

// AdminLoginResponse is the admin login response.
type AdminLoginResponse struct {
	Success          bool   `json:"success"`
	Error            string `json:"error,omitempty"`
	Locked           bool   `json:"locked,omitempty"`
	RemainingSeconds int    `json:"remaining_seconds,omitempty"`
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
	LeasesCount int    `json:"leases_count"`
	Uptime      string `json:"uptime"`
}
