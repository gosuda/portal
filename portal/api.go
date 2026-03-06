package portal

import (
	"encoding/json"
	"net/http"
	"time"
)

const (
	HeaderReverseToken = "X-Portal-Token"
	MarkerKeepalive    = byte(0x00)
	MarkerTLSStart     = byte(0x02)
)

type APIEnvelope struct {
	OK    bool      `json:"ok"`
	Data  any       `json:"data,omitempty"`
	Error *APIError `json:"error,omitempty"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type LeaseMetadata struct {
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	Thumbnail   string   `json:"thumbnail,omitempty"`
	Hide        bool     `json:"hide,omitempty"`
}

type RegisterRequest struct {
	Name         string        `json:"name"`
	Hostnames    []string      `json:"hostnames,omitempty"`
	Metadata     LeaseMetadata `json:"metadata,omitempty"`
	ReverseToken string        `json:"reverse_token"`
	TLS          bool          `json:"tls"`
	TTLSeconds   int           `json:"ttl_seconds,omitempty"`
}

type RegisterResponse struct {
	LeaseID    string        `json:"lease_id"`
	Hostnames  []string      `json:"hostnames"`
	Metadata   LeaseMetadata `json:"metadata,omitempty"`
	ExpiresAt  time.Time     `json:"expires_at"`
	ConnectURL string        `json:"connect_url"`
}

type RenewRequest struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
	TTLSeconds   int    `json:"ttl_seconds,omitempty"`
}

type RenewResponse struct {
	LeaseID   string    `json:"lease_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type UnregisterRequest struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
}

type DomainResponse struct {
	RootHost          string `json:"root_host"`
	SuggestedHostname string `json:"suggested_hostname"`
}

func writeAPIData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIEnvelope{OK: true, Data: data})
}

func writeAPIOK(w http.ResponseWriter, status int) {
	writeAPIData(w, status, map[string]any{})
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIEnvelope{
		OK:    false,
		Error: &APIError{Code: code, Message: message},
	})
}
