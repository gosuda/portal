package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/crypto/acme/autocert"
)

// CertManager handles automatic TLS certificate provisioning for funnel subdomains.
// When ACME is enabled, it provisions per-subdomain certificates via Let's Encrypt HTTP-01.
// When disabled (localhost/dev), it returns the fallback self-signed certificate.
type CertManager struct {
	manager      *autocert.Manager
	domain       string // base funnel domain (e.g. "portal.example.com")
	enabled      bool   // false for localhost/dev mode
	fallbackCert []byte // self-signed cert PEM for fallback
	fallbackKey  []byte // self-signed key PEM for fallback
}

// NewCertManager creates a CertManager. When enabled=false, GetCertPEM always
// returns the fallback cert/key (preserving existing self-signed behavior).
func NewCertManager(domain, cacheDir string, enabled bool, fallbackCert, fallbackKey []byte) *CertManager {
	cm := &CertManager{
		domain:       domain,
		enabled:      enabled,
		fallbackCert: fallbackCert,
		fallbackKey:  fallbackKey,
	}

	if enabled {
		cm.manager = &autocert.Manager{
			Cache:      autocert.DirCache(cacheDir),
			Prompt:     autocert.AcceptTOS,
			HostPolicy: cm.hostPolicy,
		}
	}

	return cm
}

// hostPolicy restricts ACME certificate provisioning to the base funnel domain
// and its subdomains. This prevents abuse by blocking requests for arbitrary domains.
func (cm *CertManager) hostPolicy(_ context.Context, host string) error {
	if host == cm.domain || strings.HasSuffix(host, "."+cm.domain) {
		return nil
	}
	return fmt.Errorf("acme: host %q is not %q or a subdomain of it", host, cm.domain)
}

// HTTPHandler returns an http.Handler that serves ACME HTTP-01 challenge
// responses on port 80. Non-challenge requests are passed to fallback.
// When ACME is disabled, returns fallback directly.
func (cm *CertManager) HTTPHandler(fallback http.Handler) http.Handler {
	if !cm.enabled || cm.manager == nil {
		return fallback
	}
	return cm.manager.HTTPHandler(fallback)
}

// GetCertPEM provisions (or retrieves cached) a TLS certificate for the given
// subdomain and returns PEM-encoded cert chain + private key for distribution
// to tunnel clients.
//
// When ACME is disabled, returns the fallback self-signed cert/key immediately.
// When ACME is enabled, this may block while the HTTP-01 challenge completes
// (typically 5-15 seconds for first provisioning; cached afterward).
func (cm *CertManager) GetCertPEM(ctx context.Context, subdomain string) (certPEM, keyPEM []byte, err error) {
	if !cm.enabled {
		return cm.fallbackCert, cm.fallbackKey, nil
	}

	fqdn := subdomain + "." + cm.domain

	// Trigger ACME provisioning via GetCertificate with a synthetic ClientHelloInfo.
	// autocert.Manager handles caching internally (memory + DirCache), so repeated
	// calls for the same domain are fast.
	hello := &tls.ClientHelloInfo{ServerName: fqdn}
	tlsCert, err := cm.manager.GetCertificate(hello)
	if err != nil {
		return nil, nil, fmt.Errorf("acme: provision cert for %s: %w", fqdn, err)
	}

	certPEM, keyPEM, err = extractPEM(tlsCert)
	if err != nil {
		return nil, nil, fmt.Errorf("acme: extract PEM for %s: %w", fqdn, err)
	}

	return certPEM, keyPEM, nil
}

// TLSConfig returns a *tls.Config that uses autocert for automatic certificate
// management. Used by the HTTPS admin server to serve the base domain.
// Returns nil when ACME is disabled.
func (cm *CertManager) TLSConfig() *tls.Config {
	if !cm.enabled || cm.manager == nil {
		return nil
	}
	return cm.manager.TLSConfig()
}

// extractPEM converts a *tls.Certificate into PEM-encoded certificate chain
// and private key bytes suitable for distribution to tunnel clients.
func extractPEM(cert *tls.Certificate) (certPEM, keyPEM []byte, err error) {
	// Encode full certificate chain (leaf + intermediates).
	for _, derBytes := range cert.Certificate {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: derBytes,
		})...)
	}

	// Encode private key using PKCS#8 (works for ECDSA, RSA, Ed25519).
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal private key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyDER,
	})

	return certPEM, keyPEM, nil
}
