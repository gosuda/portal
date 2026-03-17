package types

import (
	"fmt"
	"strings"
	"time"
)

const (
	MarkerQUICReady = byte(0x03)
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

type RegisterRequest struct {
	Name         string        `json:"name"`
	ReverseToken string        `json:"reverse_token"`
	Metadata     LeaseMetadata `json:"metadata"`
	TTL          int           `json:"ttl,omitempty"`
	TLS          bool          `json:"tls"`
	Transport    string        `json:"transport,omitempty"`
}

type RegisterResponse struct {
	ExpiresAt  time.Time     `json:"expires_at"`
	LeaseID    string        `json:"lease_id"`
	ConnectURL string        `json:"connect_url"`
	Hostname   string        `json:"hostname"`
	Metadata   LeaseMetadata `json:"metadata"`
	UDPAddr    string        `json:"udp_addr,omitempty"`
	QUICAddr   string        `json:"quic_addr,omitempty"`
	Transport  string        `json:"transport,omitempty"`
}

type RenewRequest struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
	TTL          int    `json:"ttl,omitempty"`
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
	Version string `json:"version"`
}

type AdminLoginRequest struct {
	Key string `json:"key"`
}

type AdminLoginResponse struct {
	Success bool `json:"success,omitempty"`
}

type AdminAuthStatusResponse struct {
	Authenticated bool `json:"authenticated"`
	AuthEnabled   bool `json:"auth_enabled"`
}

type AdminSnapshotResponse struct {
	ApprovalMode string  `json:"approval_mode"`
	Leases       []Lease `json:"leases,omitempty"`
}

type AdminApprovalModeRequest struct {
	Mode string `json:"mode"`
}

type AdminApprovalModeResponse struct {
	ApprovalMode string `json:"approval_mode"`
}

type AdminBPSRequest struct {
	BPS int64 `json:"bps"`
}
