// Package sdk provides a client for registering leases with the Portal relay.
package sdk

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/keyless_tls/keyless"

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
		TLSMode:            TLSModeNoTLS,
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
		TLSMode:      string(normalizeTLSMode(c.config.TLSMode)),
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
	var listenerCloseFns []func()
	tlsMode := normalizeTLSMode(c.config.TLSMode)
	tlsEnabled := tlsMode != TLSModeNoTLS
	if tlsEnabled {
		switch tlsMode {
		case TLSModeSelf:
			var cert tls.Certificate
			if c.config.TLSCertificate != nil {
				cert = *c.config.TLSCertificate
			} else {
				certFile := strings.TrimSpace(c.config.TLSSelfCertFile)
				keyFile := strings.TrimSpace(c.config.TLSSelfKeyFile)
				if certFile == "" || keyFile == "" {
					return nil, fmt.Errorf("self TLS mode requires certificate/key (WithTLSSelfCertificate or WithTLSSelfCertificateFiles)")
				}
				certPair, err := tls.LoadX509KeyPair(certFile, keyFile)
				if err != nil {
					return nil, fmt.Errorf("load self TLS certificate files: %w", err)
				}
				cert = certPair
			}
			tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
			tlsConfig.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				return &cert, nil
			}
		case TLSModeKeyless:
			keylessEndpoint := strings.TrimSpace(c.config.TLSKeyless.Endpoint)
			if keylessEndpoint == "" {
				keylessEndpoint = strings.TrimSpace(relayAddr)
			}
			keylessKeyID := strings.TrimSpace(c.config.TLSKeyless.KeyID)
			if keylessKeyID == "" {
				keylessKeyID = "relay-cert"
			}
			keylessServerName := strings.TrimSpace(c.config.TLSKeyless.ServerName)
			if keylessServerName == "" {
				if parsed, err := url.Parse(keylessEndpoint); err == nil {
					keylessServerName = parsed.Hostname()
				}
			}

			certPEM, rootCAPEM, err := resolveKeylessMaterials(
				context.Background(),
				keylessEndpoint,
				keylessServerName,
				c.config.TLSKeylessCertificatePEM,
				c.config.TLSKeyless.RootCAPEM,
			)
			if err != nil {
				return nil, fmt.Errorf("prepare keyless materials: %w", err)
			}

			baseDomain, err := fetchBaseDomain(context.Background(), relayAddr)
			if err != nil {
				return nil, fmt.Errorf("get base domain for keyless mode: %w", err)
			}
			domain := strings.ToLower(name + "." + baseDomain)
			_, leaf, err := parseCertificateChainPEM(certPEM)
			if err != nil {
				return nil, fmt.Errorf("parse keyless certificate chain: %w", err)
			}
			if err := leaf.VerifyHostname(domain); err != nil {
				return nil, fmt.Errorf("keyless certificate does not cover %s: %w", domain, err)
			}

			remoteSigner, err := keyless.NewRemoteSigner(keyless.RemoteSignerConfig{
				Endpoint:      keylessEndpoint,
				ServerName:    keylessServerName,
				KeyID:         keylessKeyID,
				EnableMTLS:    c.config.TLSKeyless.EnableMTLS,
				ClientCertPEM: c.config.TLSKeyless.ClientCertPEM,
				ClientKeyPEM:  c.config.TLSKeyless.ClientKeyPEM,
				RootCAPEM:     rootCAPEM,
			}, certPEM)
			if err != nil {
				return nil, fmt.Errorf("create keyless remote signer: %w", err)
			}
			listenerCloseFns = append(listenerCloseFns, func() {
				_ = remoteSigner.Close()
			})

			tlsConfig, err = keyless.NewServerTLSConfig(keyless.ServerTLSConfig{
				CertPEM: certPEM,
				Signer:  remoteSigner,
			})
			if err != nil {
				_ = remoteSigner.Close()
				return nil, fmt.Errorf("create keyless TLS config: %w", err)
			}
		default:
			return nil, fmt.Errorf("unsupported TLS mode: %s", tlsMode)
		}
	}

	listener, err := NewListener(relayAddr, lease, tlsConfig, c.config.ReverseWorkers, c.config.ReverseDialTimeout, listenerCloseFns...)
	if err != nil {
		return nil, fmt.Errorf("create relay listener: %w", err)
	}

	// Register lease with relay BEFORE requesting certificate
	if err := listener.Start(); err != nil {
		return nil, fmt.Errorf("start relay listener: %w", err)
	}

	c.leases[lease.ID] = lease

	if tlsEnabled {
		log.Info().
			Str("lease_id", lease.ID).
			Str("name", name).
			Bool("tls", true).
			Str("tls_mode", string(tlsMode)).
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

func parseCertificateChainPEM(certPEM []byte) ([][]byte, *x509.Certificate, error) {
	if len(certPEM) == 0 {
		return nil, nil, fmt.Errorf("certificate PEM is empty")
	}
	var chain [][]byte
	rest := certPEM
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			chain = append(chain, block.Bytes)
		}
		rest = next
	}
	if len(chain) == 0 {
		return nil, nil, fmt.Errorf("no certificate blocks found")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return nil, nil, fmt.Errorf("parse leaf certificate: %w", err)
	}
	return chain, leaf, nil
}

func fetchBaseDomain(ctx context.Context, relayAPIURL string) (string, error) {
	endpoint := strings.TrimSuffix(strings.TrimSpace(relayAPIURL), "/") + "/sdk/domain"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var domainResp struct {
		Success    bool   `json:"success"`
		BaseDomain string `json:"base_domain"`
		Message    string `json:"message"`
	}
	if err := json.Unmarshal(respBody, &domainResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if !domainResp.Success {
		msg := strings.TrimSpace(domainResp.Message)
		if msg == "" {
			msg = "base domain not configured"
		}
		return "", fmt.Errorf("get base domain: %s", msg)
	}
	return domainResp.BaseDomain, nil
}

func resolveKeylessMaterials(
	ctx context.Context,
	keylessEndpoint string,
	keylessServerName string,
	inlineCertPEM []byte,
	inlineRootCAPEM []byte,
) ([]byte, []byte, error) {
	certPEM := append([]byte(nil), inlineCertPEM...)
	chainFromEndpoint := []byte(nil)

	if len(certPEM) == 0 || len(inlineRootCAPEM) == 0 {
		autoChain, err := fetchEndpointCertificateChain(ctx, keylessEndpoint, keylessServerName)
		if err != nil && len(certPEM) == 0 {
			return nil, nil, fmt.Errorf("auto-discover certificate chain from signer endpoint: %w", err)
		}
		if err == nil {
			chainFromEndpoint = autoChain
		}
	}

	if len(certPEM) == 0 {
		certPEM = chainFromEndpoint
	}
	if len(certPEM) == 0 {
		return nil, nil, fmt.Errorf("keyless certificate chain is required")
	}

	rootCAPEM := append([]byte(nil), inlineRootCAPEM...)
	if len(rootCAPEM) == 0 && len(chainFromEndpoint) > 0 {
		rootCAPEM = append([]byte(nil), chainFromEndpoint...)
	}
	if len(rootCAPEM) == 0 {
		// Fallback for non-mTLS signer TLS verification.
		rootCAPEM = append([]byte(nil), certPEM...)
	}

	return certPEM, rootCAPEM, nil
}

func fetchEndpointCertificateChain(ctx context.Context, endpoint string, serverName string) ([]byte, error) {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return nil, fmt.Errorf("endpoint is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint URL: %w", err)
	}
	if strings.EqualFold(u.Scheme, "http") {
		return nil, fmt.Errorf("http signer endpoint does not expose TLS certificate chain (use https endpoint)")
	}

	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("endpoint hostname is empty")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	if strings.TrimSpace(serverName) == "" {
		serverName = host
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	rawConn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("dial signer endpoint: %w", err)
	}

	tlsConn := tls.Client(rawConn, &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         serverName,
		InsecureSkipVerify: true,
	})
	defer tlsConn.Close()
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("TLS handshake with signer endpoint: %w", err)
	}

	peerCerts := tlsConn.ConnectionState().PeerCertificates
	if len(peerCerts) == 0 {
		return nil, fmt.Errorf("no peer certificates from signer endpoint")
	}

	var chainPEM []byte
	for _, cert := range peerCerts {
		chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: cert.Raw,
		})...)
	}
	return chainPEM, nil
}

func normalizeTLSMode(mode TLSMode) TLSMode {
	normalized := TLSMode(strings.ToLower(strings.TrimSpace(string(mode))))
	if normalized == "" {
		return TLSModeNoTLS
	}
	return normalized
}
