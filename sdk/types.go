package sdk

import (
	"context"
	"crypto/tls"
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

type TLSMode string

const (
	TLSModeNoTLS   TLSMode = "no-tls"
	TLSModeSelf    TLSMode = "self"
	TLSModeKeyless TLSMode = "keyless"
)

type TLSKeylessConfig struct {
	Endpoint      string
	ServerName    string
	KeyID         string
	RootCAPEM     []byte
	EnableMTLS    bool
	ClientCertPEM []byte
	ClientKeyPEM  []byte
}

type ClientConfig struct {
	BootstrapServers    []string
	Dialer              func(context.Context, string) (io.ReadWriteCloser, error)
	HealthCheckInterval time.Duration // Interval for health checks (default: 10 seconds)
	ReconnectMaxRetries int           // Maximum reconnection attempts (default: 0 = infinite)
	ReconnectInterval   time.Duration // Interval between reconnection attempts (default: 5 seconds)
	ReverseWorkers      int           // Number of reverse websocket workers per listener (default: 16)
	ReverseDialTimeout  time.Duration // Reverse websocket dial timeout (default: 5 seconds)

	// TLS configuration for tunnel server mode
	TLSMode TLSMode

	// Optional local certificate used in self TLS mode.
	TLSCertificate  *tls.Certificate
	TLSSelfCertFile string
	TLSSelfKeyFile  string

	// Optional certificate chain and remote signer config used by keyless mode.
	TLSKeylessCertificatePEM []byte
	TLSKeyless               TLSKeylessConfig
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

// WithTLSSelfCertificate enables TLS with a locally managed certificate/key pair.
func WithTLSSelfCertificate(cert tls.Certificate) ClientOption {
	return func(c *ClientConfig) {
		c.TLSMode = TLSModeSelf
		copy := cert
		c.TLSCertificate = &copy
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

// WithTLSKeyless enables TLS with a local certificate chain and remote keyless signer.
func WithTLSKeyless(certPEM []byte, cfg TLSKeylessConfig) ClientOption {
	return func(c *ClientConfig) {
		c.TLSMode = TLSModeKeyless
		c.TLSKeylessCertificatePEM = append([]byte(nil), certPEM...)
		c.TLSKeyless = TLSKeylessConfig{
			Endpoint:      cfg.Endpoint,
			ServerName:    cfg.ServerName,
			KeyID:         cfg.KeyID,
			RootCAPEM:     append([]byte(nil), cfg.RootCAPEM...),
			EnableMTLS:    cfg.EnableMTLS,
			ClientCertPEM: append([]byte(nil), cfg.ClientCertPEM...),
			ClientKeyPEM:  append([]byte(nil), cfg.ClientKeyPEM...),
		}
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
