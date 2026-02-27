// Package sdk provides a client for registering leases with the Portal relay.
package sdk

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"keyless_tls/keyless"

	"github.com/rs/zerolog/log"
	"gosuda.org/portal/portal"
)

var urlSafeNameRegex = regexp.MustCompile(`^[\p{L}\p{N}_-]+$`)

// isURLSafeName checks if a name contains only URL-safe characters.
func isURLSafeName(name string) bool {
	if name == "" {
		return true
	}
	return urlSafeNameRegex.MatchString(name)
}

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
		ReverseWorkers:     0, // uses defaultReverseWorkers from listener
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
	if !isURLSafeName(name) {
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

	listener, err := NewListener(relayAddr, lease, nil, c.config.ReverseWorkers, c.config.ReverseDialTimeout)
	if err != nil {
		return nil, fmt.Errorf("create relay listener: %w", err)
	}

	// Register lease with relay BEFORE requesting certificate
	if err := listener.Start(); err != nil {
		return nil, fmt.Errorf("start relay listener: %w", err)
	}

	// For TLS mode, fetch keyless config and attach remote signer after lease registration.
	if c.config.TLSEnabled {
		keylessCfg, err := fetchKeylessConfig(context.Background(), relayAddr, lease.ID, reverseToken)
		if err != nil {
			listener.Close()
			return nil, fmt.Errorf("fetch keyless TLS config: %w", err)
		}

		remoteSigner, err := keyless.NewRemoteSigner(keyless.RemoteSignerConfig{
			Endpoint:      keylessCfg.SignerEndpoint,
			ServerName:    keylessCfg.SignerServerName,
			KeyID:         keylessCfg.KeyID,
			EnableMTLS:    keylessCfg.RequireMTLS,
			ClientCertPEM: keylessCfg.ClientCertPEM,
			ClientKeyPEM:  keylessCfg.ClientKeyPEM,
			RootCAPEM:     keylessCfg.RootCAPEM,
		}, keylessCfg.CertChainPEM)
		if err != nil {
			listener.Close()
			return nil, fmt.Errorf("initialize remote signer: %w", err)
		}

		tlsConfig, err := keyless.NewServerTLSConfig(keyless.ServerTLSConfig{
			CertPEM: keylessCfg.CertChainPEM,
			Signer:  remoteSigner,
		})
		if err != nil {
			listener.Close()
			return nil, fmt.Errorf("build keyless TLS config: %w", err)
		}

		listener.SetTLSConfig(tlsConfig, remoteSigner)
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

func fetchKeylessConfig(ctx context.Context, relayAddr, leaseID, reverseToken string) (*KeylessConfigResponse, error) {
	requestBody, err := json.Marshal(KeylessConfigRequest{LeaseID: leaseID, ReverseToken: reverseToken})
	if err != nil {
		return nil, fmt.Errorf("marshal keyless config request: %w", err)
	}

	endpoint := strings.TrimSuffix(relayAddr, "/") + "/sdk/keyless/config"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return nil, fmt.Errorf("build keyless config request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("request keyless config: %w", err)
	}
	defer resp.Body.Close()

	var keylessCfg KeylessConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&keylessCfg); err != nil {
		return nil, fmt.Errorf("decode keyless config response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if strings.TrimSpace(keylessCfg.Message) == "" {
			keylessCfg.Message = fmt.Sprintf("http status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("keyless config request failed: %s", keylessCfg.Message)
	}
	if !keylessCfg.Success {
		msg := strings.TrimSpace(keylessCfg.Message)
		if msg == "" {
			msg = "relay rejected keyless config request"
		}
		return nil, fmt.Errorf("%s", msg)
	}
	if len(keylessCfg.CertChainPEM) == 0 {
		return nil, fmt.Errorf("relay keyless config missing cert_chain_pem")
	}
	if strings.TrimSpace(keylessCfg.SignerEndpoint) == "" {
		return nil, fmt.Errorf("relay keyless config missing signer_endpoint")
	}
	if strings.TrimSpace(keylessCfg.SignerServerName) == "" {
		return nil, fmt.Errorf("relay keyless config missing signer_server_name")
	}
	if strings.TrimSpace(keylessCfg.KeyID) == "" {
		return nil, fmt.Errorf("relay keyless config missing key_id")
	}
	if len(keylessCfg.RootCAPEM) == 0 {
		return nil, fmt.Errorf("relay keyless config missing root_ca_pem")
	}

	return &keylessCfg, nil
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
