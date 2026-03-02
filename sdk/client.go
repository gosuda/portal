// Package sdk provides a client for registering leases with the Portal relay.
package sdk

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	keylesstls "github.com/gosuda/keyless_tls/keyless"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/keyless"
)

// Client is a minimal client for lease registration with the relay.
type Client struct {
	mu     sync.Mutex
	config *ClientConfig
}

// NewClient creates a new SDK client.
func NewClient(opt ...ClientOption) (*Client, error) {
	config := &ClientConfig{
		BootstrapServers:   []string{},
		ReverseWorkers:     0, // uses defaultReverseWorkers from listener
		ReverseDialTimeout: 5 * time.Second,
		TLSMode:            TLSModeNoTLS,
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
func (c *Client) Listen(name string, options ...MetadataOption) (net.Listener, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if !isURLSafeName(name) {
		return nil, ErrInvalidName
	}

	relayAddrs, err := normalizeRelayAPIURLs(c.config.BootstrapServers)
	if err != nil {
		return nil, err
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
		listener, listenerErr := NewListener(relayAddr, &leaseCopy, tlsConfig, c.config.ReverseWorkers, c.config.ReverseDialTimeout, listenerCloseFns...)
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

	if c.config.TLSMode != TLSModeNoTLS {
		log.Info().
			Str("lease_id", lease.ID).
			Str("name", name).
			Bool("tls", true).
			Str("tls_mode", string(c.config.TLSMode)).
			Msg("[SDK] Lease registered with TLS")
	} else {
		log.Info().
			Str("lease_id", lease.ID).
			Str("name", name).
			Str("tls_mode", string(TLSModeNoTLS)).
			Msg("[SDK] Lease registered")
	}

	return listener, nil
}

func (c *Client) newLease(name string, options ...MetadataOption) (*portal.Lease, error) {
	var metadata portal.Metadata
	for _, option := range options {
		option(&metadata)
	}

	reverseToken, err := generateToken(16)
	if err != nil {
		return nil, fmt.Errorf("generate reverse token: %w", err)
	}

	lease := &portal.Lease{
		ID:           generateID(),
		Name:         name,
		TLSMode:      string(c.config.TLSMode),
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
	return lease, nil
}

func ExtractBaseDomain(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}

	u, err := url.Parse(trimmed)
	if err != nil || u.Hostname() == "" {
		return ""
	}

	host := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(u.Hostname())), "*.")
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}

func normalizeRelayAPIURLs(bootstrapServers []string) ([]string, error) {
	if len(bootstrapServers) == 0 {
		return nil, ErrNoAvailableRelay
	}

	seen := make(map[string]struct{}, len(bootstrapServers))
	out := make([]string, 0, len(bootstrapServers))
	for _, relay := range bootstrapServers {
		normalized, err := normalizeRelayAPIURL(relay)
		if err != nil {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}

	if len(out) == 0 {
		return nil, ErrNoAvailableRelay
	}
	return out, nil
}

func (c *Client) buildTLSConfig(relayAddr, leaseName string) (*tls.Config, []func(), error) {
	tlsMode := c.config.TLSMode
	if tlsMode == TLSModeNoTLS {
		return nil, nil, nil
	}

	switch tlsMode {
	case TLSModeSelf:
		var cert tls.Certificate
		var err error
		if c.config.TLSCertificate != nil {
			cert = *c.config.TLSCertificate
		} else {
			if c.config.TLSSelfCertFile == "" || c.config.TLSSelfKeyFile == "" {
				return nil, nil, fmt.Errorf("self TLS mode requires certificate/key (WithTLSSelfCertificate or WithTLSSelfCertificateFiles)")
			}
			cert, err = tls.LoadX509KeyPair(c.config.TLSSelfCertFile, c.config.TLSSelfKeyFile)
			if err != nil {
				return nil, nil, fmt.Errorf("load self TLS certificate files: %w", err)
			}
		}

		tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
		tlsConfig.NextProtos = []string{"http/1.1"}
		tlsConfig.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return &cert, nil
		}
		return tlsConfig, nil, nil

	case TLSModeKeyless:
		keylessEndpoint := c.config.TLSKeyless.Endpoint
		if keylessEndpoint == "" {
			keylessEndpoint = relayAddr
		}

		keylessKeyID := c.config.TLSKeyless.KeyID
		if keylessKeyID == "" {
			keylessKeyID = "relay-cert"
		}

		keylessServerName := c.config.TLSKeyless.ServerName
		if keylessServerName == "" {
			if parsed, err := url.Parse(keylessEndpoint); err == nil {
				keylessServerName = parsed.Hostname()
			}
		}

		certPEM, rootCAPEM, err := keyless.ResolveMaterials(
			context.Background(),
			keylessEndpoint,
			keylessServerName,
			c.config.TLSKeylessCertificatePEM,
			c.config.TLSKeyless.RootCAPEM,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("prepare keyless materials: %w", err)
		}

		baseDomain := c.config.TLSKeyless.BaseDomain
		if baseDomain == "" {
			baseDomain = ExtractBaseDomain(relayAddr)
		}
		if baseDomain == "" {
			baseDomain = ExtractBaseDomain(keylessEndpoint)
		}
		if baseDomain == "" {
			return nil, nil, fmt.Errorf("keyless base domain is required for relay %s", relayAddr)
		}
		domain := leaseName + "." + baseDomain
		if err := keyless.VerifyCertificateHostname(certPEM, domain); err != nil {
			return nil, nil, fmt.Errorf("keyless certificate does not cover %s: %w", domain, err)
		}

		remoteSigner, err := keylesstls.NewRemoteSigner(keylesstls.RemoteSignerConfig{
			Endpoint:      keylessEndpoint,
			ServerName:    keylessServerName,
			KeyID:         keylessKeyID,
			EnableMTLS:    c.config.TLSKeyless.EnableMTLS,
			ClientCertPEM: c.config.TLSKeyless.ClientCertPEM,
			ClientKeyPEM:  c.config.TLSKeyless.ClientKeyPEM,
			RootCAPEM:     rootCAPEM,
		}, certPEM)
		if err != nil {
			return nil, nil, fmt.Errorf("create keyless remote signer: %w", err)
		}

		tlsConfig, err := keylesstls.NewServerTLSConfig(keylesstls.ServerTLSConfig{
			CertPEM: certPEM,
			Signer:  remoteSigner,
		})
		if err != nil {
			_ = remoteSigner.Close()
			return nil, nil, fmt.Errorf("create keyless TLS config: %w", err)
		}
		tlsConfig.NextProtos = []string{"http/1.1"}

		return tlsConfig, []func(){
			func() { _ = remoteSigner.Close() },
		}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported TLS mode: %s", tlsMode)
	}
}

// Close closes the client.
func (c *Client) Close() error {
	return nil
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
