package sdk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
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

type ClientConfig struct {
	BootstrapServers    []string
	Dialer              func(context.Context, string) (portal.Session, error)
	TLSConfig           *tls.Config   // Custom TLS config for WebTransport dialer
	HealthCheckInterval time.Duration // Interval for health checks (default: 10 seconds)
	ReconnectMaxRetries int           // Maximum reconnection attempts (default: 0 = infinite)
	ReconnectInterval   time.Duration // Interval between reconnection attempts (default: 5 seconds)
}

type ClientOption func(*ClientConfig)

func WithBootstrapServers(servers []string) ClientOption {
	return func(c *ClientConfig) {
		c.BootstrapServers = servers
	}
}

func WithDialer(dialer func(context.Context, string) (portal.Session, error)) ClientOption {
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

// WithInsecureSkipVerify disables TLS certificate verification.
// The Noise XX E2EE handshake still authenticates and encrypts all application data.
// Use for development/testing with self-signed certificates.
func WithInsecureSkipVerify() ClientOption {
	return func(c *ClientConfig) {
		c.TLSConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // user-requested; Noise E2EE provides auth
	}
}

// WithCertHash pins the relay server's TLS certificate by SHA-256 hash.
// Connections are rejected if no certificate in the chain matches the hash.
// Use with --tls-auto servers: fetch hash from /cert-hash endpoint.
func WithCertHash(hash []byte) ClientOption {
	return func(c *ClientConfig) {
		pinHash := make([]byte, len(hash))
		copy(pinHash, hash)
		c.TLSConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // custom VerifyPeerCertificate pins by cert hash
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				for _, raw := range rawCerts {
					h := sha256.Sum256(raw)
					if bytes.Equal(h[:], pinHash) {
						return nil
					}
				}
				return errors.New("portal: no certificate matches pinned hash")
			},
		}
	}
}

type Metadata struct {
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Thumbnail   string   `json:"thumbnail"`
	Owner       string   `json:"owner"`
	Hide        bool     `json:"hide"`
}

type MetadataOption func(*Metadata)

func WithDescription(description string) MetadataOption {
	return func(m *Metadata) {
		m.Description = description
	}
}

func WithTags(tags []string) MetadataOption {
	return func(m *Metadata) {
		m.Tags = tags
	}
}

func WithThumbnail(thumbnail string) MetadataOption {
	return func(m *Metadata) {
		m.Thumbnail = thumbnail
	}
}

func WithOwner(owner string) MetadataOption {
	return func(m *Metadata) {
		m.Owner = owner
	}
}

func WithHide(hide bool) MetadataOption {
	return func(m *Metadata) {
		m.Hide = hide
	}
}
