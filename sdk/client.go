// Package sdk provides a client for registering leases with the Portal relay.
package sdk

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/acme/autocert"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/utils"
)

// Client is a minimal client for lease registration with the relay.
type Client struct {
	mu     sync.Mutex
	config *ClientConfig

	leases map[string]*portal.Lease

	stopch    chan struct{}
	stopOnce  sync.Once
	waitGroup sync.WaitGroup
}

// NewClient creates a new SDK client.
func NewClient(opt ...ClientOption) (*Client, error) {
	config := &ClientConfig{
		BootstrapServers:   []string{},
		ReverseWorkers:     2,
		ReverseDialTimeout: 5 * time.Second,
	}

	for _, o := range opt {
		o(config)
	}

	return &Client{
		config: config,
		leases: make(map[string]*portal.Lease),
		stopch: make(chan struct{}),
	}, nil
}

// Listen creates a listener and registers it with the relay.
// In TLS passthrough mode, this registers the lease and returns a listener
// that accepts connections from the relay.
func (c *Client) Listen(name string, options ...MetadataOption) (net.Listener, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Validate name
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if !utils.IsURLSafeName(name) {
		return nil, ErrInvalidName
	}

	var metadata portal.Metadata
	for _, option := range options {
		option(&metadata)
	}

	relayAddr, err := firstRelayAPIURL(c.config.BootstrapServers)
	if err != nil {
		return nil, err
	}

	// Create lease
	reverseToken, err := generateToken(16)
	if err != nil {
		return nil, fmt.Errorf("generate reverse token: %w", err)
	}

	lease := &portal.Lease{
		ID:           generateID(),
		Name:         name,
		Address:      "",
		TLSEnabled:   c.config.TLSEnabled,
		ReverseToken: reverseToken,
		Metadata: portal.Metadata{
			Description: metadata.Description,
			Tags:        metadata.Tags,
			Thumbnail:   metadata.Thumbnail,
			Owner:       metadata.Owner,
			Hide:        metadata.Hide,
		},
		Expires: time.Now().Add(30 * time.Second),
	}

	// Build TLS config if enabled
	var tlsConfig *tls.Config
	var autocertMgr *autocert.Manager

	if c.config.TLSEnabled {
		tlsConfig, autocertMgr, err = buildTLSConfig(c.config)
		if err != nil {
			return nil, fmt.Errorf("build TLS config: %w", err)
		}
	}

	listener, err := NewListener(relayAddr, lease, tlsConfig, autocertMgr, c.config.ReverseWorkers, c.config.ReverseDialTimeout)
	if err != nil {
		return nil, fmt.Errorf("create relay listener: %w", err)
	}
	if err := listener.Start(); err != nil {
		return nil, fmt.Errorf("start relay listener: %w", err)
	}

	c.leases[lease.ID] = lease

	if c.config.TLSEnabled {
		log.Info().
			Str("lease_id", lease.ID).
			Str("name", name).
			Bool("tls", true).
			Msg("[SDK] Lease registered with TLS")
	} else {
		log.Info().
			Str("lease_id", lease.ID).
			Str("name", name).
			Msg("[SDK] Lease registered")
	}

	return listener, nil
}

// buildTLSConfig builds TLS configuration from client config
func buildTLSConfig(config *ClientConfig) (*tls.Config, *autocert.Manager, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	var autocertMgr *autocert.Manager

	if config.TLSAutocert {
		// Use Let's Encrypt autocert
		if config.TLSDomain == "" {
			return nil, nil, fmt.Errorf("TLS domain is required for autocert")
		}

		autocertDir := config.TLSAutocertDir
		if autocertDir == "" {
			autocertDir = "autocert-cache"
		}

		autocertMgr = &autocert.Manager{
			Cache:      autocert.DirCache(autocertDir),
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(config.TLSDomain),
		}

		tlsConfig.GetCertificate = autocertMgr.GetCertificate
		log.Info().
			Str("domain", config.TLSDomain).
			Str("cache_dir", autocertDir).
			Msg("[SDK] Using Let's Encrypt autocert for TLS")
	} else if config.TLSCert != "" && config.TLSKey != "" {
		// Use provided certificate
		cert, err := tls.LoadX509KeyPair(config.TLSCert, config.TLSKey)
		if err != nil {
			return nil, nil, fmt.Errorf("load TLS certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
		log.Info().
			Str("cert", config.TLSCert).
			Str("key", config.TLSKey).
			Msg("[SDK] Using custom TLS certificate")
	} else {
		return nil, nil, fmt.Errorf("TLS enabled but no certificate source configured (set TLSAutocert=true or provide TLSCert/TLSKey)")
	}

	return tlsConfig, autocertMgr, nil
}

// Close closes the client.
func (c *Client) Close() error {
	c.stopOnce.Do(func() {
		close(c.stopch)
	})
	c.waitGroup.Wait()
	return nil
}

func firstRelayAPIURL(bootstrapServers []string) (string, error) {
	if len(bootstrapServers) == 0 {
		return "", ErrNoAvailableRelay
	}

	for _, relay := range bootstrapServers {
		normalized, err := normalizeRelayAPIURL(relay)
		if err == nil {
			return normalized, nil
		}
	}

	return "", ErrNoAvailableRelay
}

// generateID generates a unique ID for the lease.
func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func generateToken(size int) (string, error) {
	if size <= 0 {
		size = 16
	}
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
