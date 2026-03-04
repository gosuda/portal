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
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/controlplane"
	"gosuda.org/portal/portal/keyless"
	"gosuda.org/portal/types"
)

// SDK-specific errors.
var (
	ErrNoAvailableRelay = errors.New("no available relay")
	ErrInvalidName      = errors.New("lease name must be a DNS label (letters, digits, hyphen; no dots or underscores)")
)

// ClientConfig configures the SDK client.
type ClientConfig struct {
	BootstrapServers   []string
	ReverseDialTimeout time.Duration // Reverse connect dial timeout (default: 5 seconds)
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

// Client is a minimal client for lease registration with the relay.
type Client struct {
	config *ClientConfig
	mu     sync.Mutex
}

// NewClient creates a new SDK client.
func NewClient(opt ...ClientOption) (*Client, error) {
	config := &ClientConfig{
		BootstrapServers:   []string{},
		ReverseDialTimeout: 5 * time.Second,
	}

	for _, o := range opt {
		o(config)
	}

	return &Client{config: config}, nil
}

// Listen creates a listener and registers it with the relay.
// In reverse-connect mode (TCP tunnel + TLS SNI routing), this registers the
// lease and returns a listener that accepts relay-proxied connections.
func (c *Client) Listen(name string, options ...types.MetadataOption) (net.Listener, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if name == "" {
		return nil, errors.New("name is required")
	}
	if !types.IsValidLeaseName(name) {
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
	controlPlaneIdentity, err := controlplane.IssueIdentity(lease.ID)
	if err != nil {
		return nil, err
	}

	listeners := make([]net.Listener, 0, len(relayAddrs))
	closeActiveListeners := func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}

	runCloseFns := func(closeFns []func()) {
		for _, closeFn := range closeFns {
			if closeFn != nil {
				closeFn()
			}
		}
	}

	for _, relayAddr := range relayAddrs {
		tlsConfig, listenerCloseFns, tlsErr := c.buildTLSConfig(relayAddr, name)
		if tlsErr != nil {
			closeActiveListeners()
			return nil, tlsErr
		}

		leaseCopy := *lease
		listener, listenerErr := NewListener(relayAddr, &leaseCopy, tlsConfig, controlPlaneIdentity, 0, c.config.ReverseDialTimeout, listenerCloseFns...)
		if listenerErr != nil {
			runCloseFns(listenerCloseFns)
			closeActiveListeners()
			return nil, fmt.Errorf("create relay listener: %w", listenerErr)
		}

		if startErr := listener.Start(); startErr != nil {
			_ = listener.Close()
			closeActiveListeners()
			return nil, fmt.Errorf("start relay listener: %w", startErr)
		}

		listeners = append(listeners, listener)
	}

	listener := net.Listener(newMultiRelayListener(lease.ID, listeners))
	if len(listeners) == 1 {
		listener = listeners[0]
	}

	log.Info().
		Str("lease_id", lease.ID).
		Str("name", name).
		Bool("tls", true).
		Msg("[SDK] Lease registered with TLS")

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
		TLS:          true,
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
	parsed, err := url.Parse(relayAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid relay address: %s, %w", relayAddr, err)
	}
	keylessServerName := parsed.Hostname()
	if keylessServerName == "" {
		return nil, nil, fmt.Errorf("relay hostname is required: %s", relayAddr)
	}
	baseHost := types.PortalRootHost(relayAddr)
	if baseHost == "" {
		return nil, nil, fmt.Errorf("keyless base host is required for relay %s", relayAddr)
	}
	domain := leaseName + "." + baseHost

	tlsConfig, closeFn, err := keyless.BuildClientTLSConfig(relayAddr, keylessServerName, domain)
	if err != nil {
		return nil, nil, err
	}
	return tlsConfig, []func(){closeFn}, nil
}

// Close keeps SDK lifecycle parity with callers that defer cleanup.
func (c *Client) Close() error {
	return nil
}
