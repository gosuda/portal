// Package sdk provides a client for registering leases with the Portal relay.
package sdk

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/keyless_tls/keyless/lifecycle"

	"gosuda.org/portal/portal/keyless"
	"gosuda.org/portal/types"
)

const (
	keylessDirEnvVar            = "KEYLESS_DIR"
	defaultKeylessDir           = "/etc/portal/keyless"
	keylessFullChainFile        = "fullchain.pem"
	keylessPrivateKeyFile       = "privatekey.pem"
	keylessLifecycleStateSubdir = "lifecycle-identities"
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
	if !types.IsValidServiceName(name) {
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
	controlPlaneIdentity, err := acquireLifecycleIdentity(lease.ID)
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

func (c *Client) newLease(name string, options ...types.MetadataOption) (*types.Lease, error) {
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

	lease := &types.Lease{
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

func acquireLifecycleIdentity(leaseID string) (tls.Certificate, error) {
	manager, err := newLifecycleManager()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("initialize keyless lifecycle manager: %w", err)
	}

	ctx := context.Background()
	bundle, err := loadOrAcquireLifecycleIdentityBundle(ctx, manager, leaseID)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("acquire lifecycle identity for lease %s: %w", leaseID, err)
	}

	cert, leaf, _, err := decodeLifecycleIdentityBundleWithReissue(ctx, manager, leaseID, bundle)
	if err != nil {
		return tls.Certificate{}, err
	}

	if _, err := manager.ValidateIdentity(leaseID, leaf); err != nil {
		bundle, err = repairLifecycleIdentityBundle(ctx, manager, leaseID, err)
		if err != nil {
			return tls.Certificate{}, err
		}

		cert, leaf, err = tlsCertificateFromLifecycleBundle(bundle)
		if err != nil {
			return tls.Certificate{}, err
		}
		if _, err := manager.ValidateIdentity(leaseID, leaf); err != nil {
			return tls.Certificate{}, fmt.Errorf("validate renewed lifecycle identity for lease %s: %w", leaseID, err)
		}
	}

	return cert, nil
}

func loadOrAcquireLifecycleIdentityBundle(ctx context.Context, manager *lifecycle.Manager, leaseID string) (*lifecycle.IdentityBundle, error) {
	bundle, err := manager.LoadIdentity(ctx, leaseID)
	switch {
	case errors.Is(err, lifecycle.ErrLeaseNotFound):
		bundle, err = manager.IssueIdentity(ctx, leaseID, lifecycle.ChallengeProof{}, nil)
	case errors.Is(err, lifecycle.ErrCorruptStore):
		bundle, err = manager.ReissueIdentity(ctx, leaseID, lifecycle.ChallengeProof{}, "corrupt_store")
	}
	return bundle, err
}

func decodeLifecycleIdentityBundleWithReissue(
	ctx context.Context,
	manager *lifecycle.Manager,
	leaseID string,
	bundle *lifecycle.IdentityBundle,
) (tls.Certificate, *x509.Certificate, *lifecycle.IdentityBundle, error) {
	cert, leaf, err := tlsCertificateFromLifecycleBundle(bundle)
	if err == nil {
		return cert, leaf, bundle, nil
	}

	reissued, reissueErr := manager.ReissueIdentity(ctx, leaseID, lifecycle.ChallengeProof{}, "bundle_parse_failure")
	if reissueErr != nil {
		return tls.Certificate{}, nil, nil, fmt.Errorf("decode lifecycle identity for lease %s: %w", leaseID, err)
	}

	cert, leaf, err = tlsCertificateFromLifecycleBundle(reissued)
	if err != nil {
		return tls.Certificate{}, nil, nil, fmt.Errorf("decode reissued lifecycle identity for lease %s: %w", leaseID, err)
	}
	return cert, leaf, reissued, nil
}

func repairLifecycleIdentityBundle(
	ctx context.Context,
	manager *lifecycle.Manager,
	leaseID string,
	validateErr error,
) (*lifecycle.IdentityBundle, error) {
	var (
		bundle *lifecycle.IdentityBundle
		err    error
	)

	switch {
	case errors.Is(validateErr, lifecycle.ErrCorruptStore):
		bundle, err = manager.ReissueIdentity(ctx, leaseID, lifecycle.ChallengeProof{}, "validate_corrupt_store")
	case errors.Is(validateErr, lifecycle.ErrInvalidCert), errors.Is(validateErr, lifecycle.ErrOverlapExpired):
		bundle, err = manager.RenewIdentity(ctx, leaseID)
		if errors.Is(err, lifecycle.ErrCorruptStore) {
			bundle, err = manager.ReissueIdentity(ctx, leaseID, lifecycle.ChallengeProof{}, "renew_corrupt_store")
		}
	default:
		return nil, fmt.Errorf("validate lifecycle identity for lease %s: %w", leaseID, validateErr)
	}
	if err != nil {
		return nil, fmt.Errorf("repair lifecycle identity for lease %s: %w", leaseID, err)
	}

	return bundle, nil
}

func newLifecycleManager() (*lifecycle.Manager, error) {
	keylessDir := strings.TrimSpace(os.Getenv(keylessDirEnvVar))
	if keylessDir == "" {
		keylessDir = defaultKeylessDir
	}

	certPath := filepath.Join(keylessDir, keylessFullChainFile)
	keyPath := filepath.Join(keylessDir, keylessPrivateKeyFile)
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read keyless issuer certificate %q: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read keyless issuer private key %q: %w", keyPath, err)
	}
	if _, err = tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return nil, fmt.Errorf("load keyless issuer key pair from %q and %q: %w", certPath, keyPath, err)
	}

	secret := sha256.Sum256(keyPEM)
	storeDir := filepath.Join(keylessDir, keylessLifecycleStateSubdir)
	store, err := lifecycle.NewDiskStore(storeDir, secret[:])
	if err != nil {
		return nil, fmt.Errorf("create keyless lifecycle store %q: %w", storeDir, err)
	}

	manager, err := lifecycle.NewManager(lifecycle.ManagerConfig{
		Store:         store,
		IssuerCertPEM: certPEM,
		IssuerKeyPEM:  keyPEM,
	})
	if err != nil {
		return nil, fmt.Errorf("create keyless lifecycle manager: %w", err)
	}
	return manager, nil
}

func tlsCertificateFromLifecycleBundle(bundle *lifecycle.IdentityBundle) (tls.Certificate, *x509.Certificate, error) {
	if bundle == nil {
		return tls.Certificate{}, nil, errors.New("lifecycle identity bundle is required")
	}
	if len(bundle.ChainPEM) == 0 {
		return tls.Certificate{}, nil, errors.New("lifecycle identity certificate chain is empty")
	}
	if len(bundle.KeyPEM) == 0 {
		return tls.Certificate{}, nil, errors.New("lifecycle identity private key is empty")
	}

	cert, err := tls.X509KeyPair(bundle.ChainPEM, bundle.KeyPEM)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load lifecycle identity key pair for lease %s: %w", bundle.LeaseID, err)
	}
	if len(cert.Certificate) == 0 {
		return tls.Certificate{}, nil, fmt.Errorf("lifecycle identity certificate chain missing leaf for lease %s", bundle.LeaseID)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("parse lifecycle identity leaf certificate for lease %s: %w", bundle.LeaseID, err)
	}
	return cert, leaf, nil
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
