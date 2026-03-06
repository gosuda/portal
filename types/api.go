package types

import "time"

const (
	HeaderReverseToken = "X-Portal-Token"
	MarkerKeepalive    = byte(0x00)
	MarkerTLSStart     = byte(0x02)
)

type APIEnvelope struct {
	Data  any       `json:"data,omitempty"`
	Error *APIError `json:"error,omitempty"`
	OK    bool      `json:"ok"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type LeaseMetadata struct {
	Description string   `json:"description,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	Thumbnail   string   `json:"thumbnail,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Hide        bool     `json:"hide,omitempty"`
}

type RegisterRequest struct {
	Name         string        `json:"name"`
	ReverseToken string        `json:"reverse_token"`
	Hostnames    []string      `json:"hostnames,omitempty"`
	Metadata     LeaseMetadata `json:"metadata,omitempty"`
	TTLSeconds   int           `json:"ttl_seconds,omitempty"`
	TLS          bool          `json:"tls"`
}

type RegisterResponse struct {
	ExpiresAt  time.Time     `json:"expires_at"`
	LeaseID    string        `json:"lease_id"`
	ConnectURL string        `json:"connect_url"`
	Hostnames  []string      `json:"hostnames"`
	Metadata   LeaseMetadata `json:"metadata,omitempty"`
}

type RenewRequest struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
	TTLSeconds   int    `json:"ttl_seconds,omitempty"`
}

type RenewResponse struct {
	ExpiresAt time.Time `json:"expires_at"`
	LeaseID   string    `json:"lease_id"`
}

type UnregisterRequest struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
}

type DomainResponse struct {
	RootHost          string `json:"root_host"`
	SuggestedHostname string `json:"suggested_hostname"`
}

type AdminLoginRequest struct {
	Key string `json:"key"`
}

type AdminLoginResponse struct {
	Success          bool `json:"success,omitempty"`
	Locked           bool `json:"locked,omitempty"`
	RemainingSeconds int  `json:"remaining_seconds,omitempty"`
}

type AdminAuthStatusResponse struct {
	Authenticated bool `json:"authenticated"`
	AuthEnabled   bool `json:"auth_enabled"`
}

type AdminApprovalModeRequest struct {
	Mode string `json:"mode"`
}

type AdminApprovalModeResponse struct {
	ApprovalMode string `json:"approval_mode"`
}

type AdminSettingsResponse struct {
	ApprovalMode   string   `json:"approval_mode"`
	ApprovedLeases []string `json:"approved_leases,omitempty"`
	DeniedLeases   []string `json:"denied_leases,omitempty"`
}
