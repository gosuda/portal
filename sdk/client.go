// Package sdk provides a client for registering leases with the Portal relay.
package sdk

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/keyless"
	"gosuda.org/portal/types"
)

// SDK-specific errors.
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

// ClientConfig configures the SDK client.
type ClientConfig struct {
	BootstrapServers   []string
	ReverseDialTimeout time.Duration // Reverse websocket dial timeout (default: 5 seconds)
	TLS                bool
}

// ClientOption configures ClientConfig.
type ClientOption func(*ClientConfig)

// WithBootstrapServers sets the bootstrap relay servers.
func WithBootstrapServers(servers []string) ClientOption {
	return func(c *ClientConfig) {
		c.BootstrapServers = servers
	}
}

// WithReverseDialTimeout sets the reverse dial timeout.
func WithReverseDialTimeout(timeout time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.ReverseDialTimeout = timeout
	}
}

// WithTLS enables keyless TLS mode using relay-derived defaults.
func WithTLS() ClientOption {
	return func(c *ClientConfig) {
		c.TLS = true
	}
}

// Client is a minimal client for lease registration with the relay.
type Client struct {
	mu     sync.Mutex
	config *ClientConfig
}

// NewClient creates a new SDK client.
func NewClient(opt ...ClientOption) (*Client, error) {
	config := &ClientConfig{
		BootstrapServers:   []string{},
		ReverseDialTimeout: 5 * time.Second,
		TLS:                false,
	}

	for _, o := range opt {
		o(config)
	}

	return &Client{config: config}, nil
}

var urlSafeNameRegex = regexp.MustCompile(`^[\p{L}\p{N}_-]+$`)

// isURLSafeName checks if a name contains only URL-safe characters.
func isURLSafeName(name string) bool {
	if name == "" {
		return true
	}
	return urlSafeNameRegex.MatchString(name)
}

// Listen creates a listener and registers it with the relay.
// In TLS passthrough mode, this registers the lease and returns a listener
// that accepts connections from the relay.
func (c *Client) Listen(name string, options ...types.MetadataOption) (net.Listener, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if !isURLSafeName(name) {
		return nil, ErrInvalidName
	}

	relayAddrs, err := types.NormalizeRelayAPIURLs(c.config.BootstrapServers)
	if err != nil {
		return nil, ErrNoAvailableRelay
	}

	lease, err := c.newLease(name, options...)
	if err != nil {
		return nil, err
	}

	listeners := make([]net.Listener, 0, len(relayAddrs))
	for _, relayAddr := range relayAddrs {
		tlsConfig, listenerCloseFns, tlsErr := c.buildTLSConfig(relayAddr, name)
		if tlsErr != nil {
			for _, l := range listeners {
				_ = l.Close()
			}
			return nil, tlsErr
		}

		leaseCopy := *lease
		listener, listenerErr := NewListener(relayAddr, &leaseCopy, tlsConfig, 0, c.config.ReverseDialTimeout, listenerCloseFns...)
		if listenerErr != nil {
			for _, closeFn := range listenerCloseFns {
				if closeFn != nil {
					closeFn()
				}
			}
			for _, l := range listeners {
				_ = l.Close()
			}
			return nil, fmt.Errorf("create relay listener: %w", listenerErr)
		}

		if startErr := listener.Start(); startErr != nil {
			_ = listener.Close()
			for _, l := range listeners {
				_ = l.Close()
			}
			return nil, fmt.Errorf("start relay listener: %w", startErr)
		}

		listeners = append(listeners, listener)
	}

	var listener net.Listener
	if len(listeners) == 1 {
		listener = listeners[0]
	} else {
		listener = newMultiRelayListener(lease.ID, listeners)
	}

	if c.config.TLS {
		log.Info().
			Str("lease_id", lease.ID).
			Str("name", name).
			Bool("tls", true).
			Msg("[SDK] Lease registered with TLS")
	} else {
		log.Info().
			Str("lease_id", lease.ID).
			Str("name", name).
			Bool("tls", false).
			Msg("[SDK] Lease registered")
	}

	return listener, nil
}

func (c *Client) newLease(name string, options ...types.MetadataOption) (*portal.Lease, error) {
	var metadata types.Metadata
	for _, option := range options {
		option(&metadata)
	}

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, fmt.Errorf("generate lease ID: %w", err)
	}

	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate reverse token: %w", err)
	}

	lease := &portal.Lease{
		ID:           hex.EncodeToString(idBytes),
		Name:         name,
		TLS:          c.config.TLS,
		ReverseToken: hex.EncodeToString(tokenBytes),
		Metadata: types.Metadata{
			Description: metadata.Description,
			Tags:        metadata.Tags,
			Thumbnail:   metadata.Thumbnail,
			Owner:       metadata.Owner,
			Hide:        metadata.Hide,
		},
		Expires: time.Now().Add(30 * time.Second),
	}
	return lease, nil
}

func (c *Client) buildTLSConfig(relayAddr, leaseName string) (*tls.Config, []func(), error) {
	if !c.config.TLS {
		return nil, nil, nil
	}

	parsed, err := url.Parse(relayAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid relay address: %s, %w", relayAddr, err)
	}
	keylessServerName := parsed.Hostname()
	if keylessServerName == "" {
		return nil, nil, fmt.Errorf("relay hostname is required: %s", relayAddr)
	}
	baseDomain := types.ExtractBaseDomain(relayAddr)
	if baseDomain == "" {
		return nil, nil, fmt.Errorf("keyless base domain is required for relay %s", relayAddr)
	}
	domain := leaseName + "." + baseDomain

	tlsConfig, closeFn, err := keyless.BuildClientTLSConfig(relayAddr, keylessServerName, domain)
	if err != nil {
		return nil, nil, err
	}
	return tlsConfig, []func(){closeFn}, nil
}

// Close closes the client.
func (c *Client) Close() error {
	return nil
}
