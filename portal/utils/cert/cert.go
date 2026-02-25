package cert

import (
	"context"
	"time"
)

// Certificate represents an issued certificate chain.
type Certificate struct {
	Domain      string    // The domain name (e.g., "app1.portal.com")
	Certificate []byte    // PEM-encoded certificate chain
	IssuedAt    time.Time // When the certificate was issued
	ExpiresAt   time.Time // When the certificate expires
}

// CSRRequest contains the data needed to issue a certificate.
type CSRRequest struct {
	Domain string // The domain to issue for (e.g., "app1.portal.com")
	CSR    []byte // PEM-encoded Certificate Signing Request
}

// Manager handles certificate issuance via ACME with DNS-01 challenge.
type Manager interface {
	// IssueCertificate issues a certificate for the given CSR.
	// The private key remains with the caller; only the cert chain is returned.
	IssueCertificate(ctx context.Context, req *CSRRequest) (*Certificate, error)

	// GetCACertificate returns the CA certificate for verification.
	GetCACertificate(ctx context.Context) ([]byte, error)
}

// DNSProvider handles DNS-01 challenge lifecycle.
// Signature matches lego's challenge.Provider.
type DNSProvider interface {
	// Present creates a TXT record for the DNS-01 challenge.
	Present(domain, token, keyAuth string) error

	// CleanUp removes the TXT record after the challenge is complete.
	CleanUp(domain, token, keyAuth string) error

	// Timeout returns the timeout and interval for DNS propagation checking.
	Timeout() (timeout time.Duration, interval time.Duration)
}
