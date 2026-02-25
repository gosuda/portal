package sdk

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// AutoCertManager manages TLS certificates obtained via relay's ACME DNS-01.
// It generates the private key locally, creates a CSR, and requests the certificate
// from the relay. The private key never leaves the tunnel.
type AutoCertManager struct {
	relayURL     string
	leaseName    string
	leaseID      string
	reverseToken string

	mu        sync.RWMutex
	keyPair   *KeyPair
	domain    string
	certPEM   []byte
	cert      atomic.Pointer[tls.Certificate]
	expiresAt time.Time

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewAutoCertManager creates a new auto certificate manager.
// The domain is derived from leaseName + relay's base domain.
func NewAutoCertManager(relayURL, leaseName, leaseID, reverseToken string) *AutoCertManager {
	return &AutoCertManager{
		relayURL:     relayURL,
		leaseName:    leaseName,
		leaseID:      leaseID,
		reverseToken: reverseToken,
		stopCh:       make(chan struct{}),
	}
}

// Initialize generates the key pair and obtains the initial certificate.
func (m *AutoCertManager) Initialize(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Fetch base domain from relay
	client := NewCertificateClient(m.relayURL)
	baseDomain, err := client.GetBaseDomain(ctx)
	if err != nil {
		return fmt.Errorf("get base domain: %w", err)
	}

	// Construct full domain
	m.domain = m.leaseName + "." + baseDomain

	// Generate key pair (private key stays local)
	keyPair, err := GenerateKeyPair()
	if err != nil {
		return fmt.Errorf("generate key pair: %w", err)
	}
	m.keyPair = keyPair

	// Create CSR with the domain
	csrPEM, err := CreateCSR(keyPair, m.domain)
	if err != nil {
		return fmt.Errorf("create CSR: %w", err)
	}

	// Request certificate from relay
	resp, err := client.RequestCertificate(ctx, m.leaseID, m.reverseToken, csrPEM)
	if err != nil {
		return fmt.Errorf("request certificate: %w", err)
	}

	m.certPEM = resp.Certificate
	if resp.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, resp.ExpiresAt); err == nil {
			m.expiresAt = t
		}
	}

	// Build tls.Certificate
	cert, err := m.buildCertificate()
	if err != nil {
		return fmt.Errorf("build certificate: %w", err)
	}
	m.cert.Store(&cert)

	log.Info().
		Str("domain", m.domain).
		Time("expires", m.expiresAt).
		Msg("[SDK] Auto certificate obtained")

	return nil
}

// GetCertificate returns a tls.GetCertificateFunc for use with tls.Config.
func (m *AutoCertManager) GetCertificate() func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		cert := m.cert.Load()
		if cert == nil {
			return nil, fmt.Errorf("certificate not available")
		}
		return cert, nil
	}
}

// StartRenewal starts a background goroutine to renew the certificate before expiry.
// Renewal occurs at 2/3 of the certificate lifetime.
func (m *AutoCertManager) StartRenewal() {
	m.wg.Add(1)
	go m.renewalLoop()
}

// Stop stops the renewal goroutine.
func (m *AutoCertManager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	m.wg.Wait()
}

func (m *AutoCertManager) renewalLoop() {
	defer m.wg.Done()

	// Check every hour
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			if m.shouldRenew() {
				if err := m.renew(); err != nil {
					log.Warn().Err(err).Msg("[SDK] Certificate renewal failed")
				}
			}
		}
	}
}

func (m *AutoCertManager) shouldRenew() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.expiresAt.IsZero() {
		return false
	}

	// Renew at 2/3 of lifetime
	now := time.Now()
	renewAt := m.expiresAt.Add(-m.expiresAt.Sub(now) / 3)
	return now.After(renewAt) || now.Equal(renewAt)
}

func (m *AutoCertManager) renew() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Create CSR with existing key
	csrPEM, err := CreateCSR(m.keyPair, m.domain)
	if err != nil {
		return fmt.Errorf("create CSR: %w", err)
	}

	// Request certificate from relay
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client := NewCertificateClient(m.relayURL)
	resp, err := client.RequestCertificate(ctx, m.leaseID, m.reverseToken, csrPEM)
	if err != nil {
		return fmt.Errorf("request certificate: %w", err)
	}

	m.certPEM = resp.Certificate
	if resp.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, resp.ExpiresAt); err == nil {
			m.expiresAt = t
		}
	}

	// Build new tls.Certificate
	cert, err := m.buildCertificate()
	if err != nil {
		return fmt.Errorf("build certificate: %w", err)
	}
	m.cert.Store(&cert)

	log.Info().
		Str("domain", m.domain).
		Time("expires", m.expiresAt).
		Msg("[SDK] Certificate renewed")

	return nil
}

func (m *AutoCertManager) buildCertificate() (tls.Certificate, error) {
	keyPEM, err := PrivateKeyToPEM(m.keyPair.PrivateKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("encode private key: %w", err)
	}

	cert, err := tls.X509KeyPair(m.certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create X509KeyPair: %w", err)
	}

	// Parse leaf certificate for logging/debugging
	if len(cert.Certificate) > 0 {
		if leaf, err := x509.ParseCertificate(cert.Certificate[0]); err == nil {
			cert.Leaf = leaf
		}
	}

	return cert, nil
}
