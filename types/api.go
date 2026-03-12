package types

import (
	"fmt"
	"strings"
	"time"
)

const (
	HeaderReverseToken = "X-Portal-Token"
	MarkerKeepalive    = byte(0x00)
	MarkerTLSStart     = byte(0x02)
	MarkerQUICReady    = byte(0x03)
)

const (
	TransportTCP  = "tcp"
	TransportUDP  = "udp"
	TransportBoth = "both"
)

type APIEnvelope[T any] struct {
	Data  T         `json:"data,omitempty"`
	Error *APIError `json:"error,omitempty"`
	OK    bool      `json:"ok"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type APIRequestError struct {
	StatusCode int    `json:"-"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
}

func (e *APIRequestError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Code) != "" {
		return e.Code + ": " + strings.TrimSpace(e.Message)
	}
	if strings.TrimSpace(e.Message) != "" {
		return strings.TrimSpace(e.Message)
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("api request failed with status %d", e.StatusCode)
	}
	return "api request failed"
}

func (e *APIRequestError) Is(target error) bool {
	other, ok := target.(*APIRequestError)
	if !ok {
		return false
	}
	if other.Code != "" && e.Code != other.Code {
		return false
	}
	if other.StatusCode != 0 && e.StatusCode != other.StatusCode {
		return false
	}
	return true
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
	Metadata     LeaseMetadata `json:"metadata"`
	TTLSeconds   int           `json:"ttl_seconds,omitempty"`
	TLS          bool          `json:"tls"`
	Transport    string        `json:"transport,omitempty"`
}

type RegisterResponse struct {
	ExpiresAt  time.Time     `json:"expires_at"`
	LeaseID    string        `json:"lease_id"`
	ConnectURL string        `json:"connect_url"`
	Hostnames  []string      `json:"hostnames"`
	Metadata   LeaseMetadata `json:"metadata"`
	UDPAddr    string        `json:"udp_addr,omitempty"`
	QUICAddr   string        `json:"quic_addr,omitempty"`
	Transport  string        `json:"transport,omitempty"`
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
