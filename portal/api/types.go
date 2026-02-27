// Package api defines shared request/response types for the Portal funnel REST API.
package api

// RegisterRequest is the body for POST /api/register.
type RegisterRequest struct {
	Name     string `json:"name"`
	Metadata string `json:"metadata,omitempty"`
}

// RegisterResponse is the response for POST /api/register.
type RegisterResponse struct {
	Success      bool   `json:"success"`
	Message      string `json:"message,omitempty"`
	LeaseID      string `json:"lease_id,omitempty"`
	ReverseToken string `json:"reverse_token,omitempty"`
	PublicURL    string `json:"public_url,omitempty"`
	TLSCert      string `json:"tls_cert,omitempty"`
	TLSKey       string `json:"tls_key,omitempty"`
}

// RenewRequest is the body for POST /api/renew.
type RenewRequest struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
}

// RenewResponse is the response for POST /api/renew.
type RenewResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	TLSCert string `json:"tls_cert,omitempty"`
	TLSKey  string `json:"tls_key,omitempty"`
}

// UnregisterRequest is the body for POST /api/unregister.
type UnregisterRequest struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
}

// UnregisterResponse is the response for POST /api/unregister.
type UnregisterResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}
