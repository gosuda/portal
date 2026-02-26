package sdk

import (
	"context"
	"errors"
	"io"
	"time"

	"gosuda.org/portal/portal"
)

var (
	ErrNoAvailableRelay     = errors.New("no available relay")
	ErrClientClosed         = errors.New("client is closed")
	ErrListenerExists       = errors.New("listener already exists for this credential")
	ErrRelayExists          = errors.New("relay already exists")
	ErrRelayNotFound        = errors.New("relay not found")
	ErrInvalidName          = errors.New("lease name contains invalid characters (only alphanumeric, hyphen, underscore allowed)")
	ErrFailedToCreateClient = errors.New("failed to create relay client")
	ErrInvalidMetadata      = errors.New("invalid metadata")
)

type ClientConfig struct {
	BootstrapServers    []string
	Dialer              func(context.Context, string) (io.ReadWriteCloser, error)
	HealthCheckInterval time.Duration // Interval for health checks (default: 10 seconds)
	ReconnectMaxRetries int           // Maximum reconnection attempts (default: 0 = infinite)
	ReconnectInterval   time.Duration // Interval between reconnection attempts (default: 5 seconds)
	ReverseWorkers      int           // Number of reverse websocket workers per listener (default: 16)
	ReverseDialTimeout  time.Duration // Reverse websocket dial timeout (default: 5 seconds)

	// TLS configuration for tunnel server mode
	TLSEnabled bool // Enable TLS listener
}

type ClientOption func(*ClientConfig)

func WithBootstrapServers(servers []string) ClientOption {
	return func(c *ClientConfig) {
		c.BootstrapServers = servers
	}
}

func WithDialer(dialer func(context.Context, string) (io.ReadWriteCloser, error)) ClientOption {
	return func(c *ClientConfig) {
		c.Dialer = dialer
	}
}

func WithHealthCheckInterval(interval time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.HealthCheckInterval = interval
	}
}

func WithReconnectMaxRetries(retries int) ClientOption {
	return func(c *ClientConfig) {
		c.ReconnectMaxRetries = retries
	}
}

func WithReconnectInterval(interval time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.ReconnectInterval = interval
	}
}

func WithReverseWorkers(workers int) ClientOption {
	return func(c *ClientConfig) {
		c.ReverseWorkers = workers
	}
}

func WithReverseDialTimeout(timeout time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.ReverseDialTimeout = timeout
	}
}

// WithTLS enables TLS with certificate issued via relay's ACME DNS-01.
// The domain is derived from the lease name and relay's base domain.
// The private key is generated locally and never leaves the tunnel.
func WithTLS() ClientOption {
	return func(c *ClientConfig) {
		c.TLSEnabled = true
	}
}

// MetadataOption configures Metadata
type MetadataOption func(*portal.Metadata)

func WithDescription(description string) MetadataOption {
	return func(m *portal.Metadata) {
		m.Description = description
	}
}

func WithTags(tags []string) MetadataOption {
	return func(m *portal.Metadata) {
		m.Tags = tags
	}
}

func WithThumbnail(thumbnail string) MetadataOption {
	return func(m *portal.Metadata) {
		m.Thumbnail = thumbnail
	}
}

func WithOwner(owner string) MetadataOption {
	return func(m *portal.Metadata) {
		m.Owner = owner
	}
}

func WithHide(hide bool) MetadataOption {
	return func(m *portal.Metadata) {
		m.Hide = hide
	}
}

// API Types for /sdk/ endpoints
// These types are shared between SDK and relay server
type RegisterRequest struct {
	LeaseID      string          `json:"lease_id"`
	Name         string          `json:"name"`
	Metadata     portal.Metadata `json:"metadata"`
	TLSEnabled   bool            `json:"tls_enabled"` // Whether the backend handles TLS termination
	ReverseToken string          `json:"reverse_token"`
}

type RegisterResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message,omitempty"`
	LeaseID   string `json:"lease_id,omitempty"`
	PublicURL string `json:"public_url,omitempty"`
}

type UnregisterRequest struct {
	LeaseID string `json:"lease_id"`
}

type RenewRequest struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
}

type APIResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// CSRRequest represents a Certificate Signing Request submission
type CSRRequest struct {
	LeaseID      string `json:"lease_id"`
	ReverseToken string `json:"reverse_token"`
	CSR          []byte `json:"csr"` // PEM-encoded Certificate Signing Request
}

// CSRResponse represents the response to a CSR submission
type CSRResponse struct {
	Success     bool   `json:"success"`
	Message     string `json:"message,omitempty"`
	Certificate []byte `json:"certificate,omitempty"` // PEM-encoded certificate chain
	ExpiresAt   string `json:"expires_at,omitempty"`  // ISO 8601 timestamp
}
