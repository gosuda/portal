package sdk

import (
	"errors"
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

type TLSMode string

const (
	TLSModeNoTLS   TLSMode = "no-tls"
	TLSModeSelf    TLSMode = "self"
	TLSModeKeyless TLSMode = "keyless"
)

type ClientConfig struct {
	BootstrapServers   []string
	ReverseDialTimeout time.Duration // Reverse websocket dial timeout (default: 5 seconds)

	TLSMode TLSMode

	// Self TLS mode certificate/key file paths.
	TLSSelfCertFile string
	TLSSelfKeyFile  string

	// Optional keyless overrides.
	// If endpoint is empty, SDK uses the relay URL.
	TLSKeylessEndpoint string
	// If base domain is empty, SDK derives it from relay or signer endpoint.
	TLSKeylessBaseDomain string
}

type ClientOption func(*ClientConfig)

func WithBootstrapServers(servers []string) ClientOption {
	return func(c *ClientConfig) {
		c.BootstrapServers = servers
	}
}

func WithReverseDialTimeout(timeout time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.ReverseDialTimeout = timeout
	}
}

// WithTLSSelfCertificateFiles enables self TLS mode using certificate/key file paths.
func WithTLSSelfCertificateFiles(certFile, keyFile string) ClientOption {
	return func(c *ClientConfig) {
		c.TLSMode = TLSModeSelf
		c.TLSSelfCertFile = certFile
		c.TLSSelfKeyFile = keyFile
	}
}

// WithTLSKeyless enables keyless TLS mode with optional signer overrides.
func WithTLSKeyless(endpoint, baseDomain string) ClientOption {
	return func(c *ClientConfig) {
		c.TLSMode = TLSModeKeyless
		c.TLSKeylessEndpoint = endpoint
		c.TLSKeylessBaseDomain = baseDomain
	}
}

// WithTLSKeylessDefaults enables keyless TLS mode with SDK-managed defaults.
// Certificate chain and signer trust are auto-discovered from signer endpoint when not explicitly provided.
func WithTLSKeylessDefaults() ClientOption {
	return func(c *ClientConfig) {
		c.TLSMode = TLSModeKeyless
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
	TLSMode      TLSMode         `json:"tls_mode"` // no-tls, self, keyless
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
