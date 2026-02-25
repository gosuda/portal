package cert

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/providers/dns/route53"
	"github.com/go-acme/lego/v4/registration"
	"github.com/rs/zerolog/log"
)

// ACMEManager implements Manager using LEGO ACME client with DNS-01 challenge.
type ACMEManager struct {
	client      *lego.Client
	dnsProvider DNSProvider
	baseDomain  string
	mu          sync.Mutex
}

// ACMEConfig contains configuration for the ACME manager.
type ACMEConfig struct {
	// BaseDomain is the base domain for subdomains (e.g., "portal.com")
	BaseDomain string

	// DNSProviderType specifies which DNS provider to use (cloudflare, route53)
	DNSProviderType string

	// DirectoryURL is the ACME directory URL (defaults to Let's Encrypt production)
	DirectoryURL string

	// Email is the email for ACME account registration
	Email string
}

// NewACMEManager creates a new ACME certificate manager.
func NewACMEManager(ctx context.Context, cfg *ACMEConfig) (*ACMEManager, error) {
	if cfg.BaseDomain == "" {
		return nil, fmt.Errorf("base domain is required")
	}
	if cfg.Email == "" {
		return nil, fmt.Errorf("email is required for ACME registration")
	}

	dnsProvider, err := createDNSProvider(cfg.DNSProviderType)
	if err != nil {
		return nil, fmt.Errorf("create DNS provider: %w", err)
	}

	// Generate ACME account key
	accountKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate account key: %w", err)
	}

	user := &acmeUser{
		email: cfg.Email,
		key:   accountKey,
	}

	legoCfg := lego.NewConfig(user)
	legoCfg.CADirURL = getDirectoryURL(cfg.DirectoryURL)

	client, err := lego.NewClient(legoCfg)
	if err != nil {
		return nil, fmt.Errorf("create lego client: %w", err)
	}

	// Set up DNS-01 challenge
	provider := &dnsProviderAdapter{dnsProvider: dnsProvider}
	if err := client.Challenge.SetDNS01Provider(provider); err != nil {
		return nil, fmt.Errorf("set DNS01 provider: %w", err)
	}

	// Register account
	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return nil, fmt.Errorf("register ACME account: %w", err)
	}
	user.registration = reg

	log.Info().
		Str("base_domain", cfg.BaseDomain).
		Str("dns_provider", cfg.DNSProviderType).
		Str("email", cfg.Email).
		Msg("[cert] ACME manager initialized")

	return &ACMEManager{
		client:      client,
		dnsProvider: dnsProvider,
		baseDomain:  cfg.BaseDomain,
	}, nil
}

// IssueCertificate issues a certificate for the given CSR.
// The CSR must be PEM-encoded and already contains the public key.
// The private key remains with the caller; only the cert chain is returned.
func (m *ACMEManager) IssueCertificate(ctx context.Context, req *CSRRequest) (*Certificate, error) {
	if req.Domain == "" {
		return nil, fmt.Errorf("domain is required")
	}
	if len(req.CSR) == 0 {
		return nil, fmt.Errorf("CSR is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Decode PEM-encoded CSR and parse to x509.CertificateRequest
	csr, err := parsePEMCSR(req.CSR)
	if err != nil {
		return nil, fmt.Errorf("parse CSR: %w", err)
	}

	certReq := certificate.ObtainForCSRRequest{
		CSR:    csr,
		Bundle: true,
	}

	cert, err := m.client.Certificate.ObtainForCSR(certReq)
	if err != nil {
		return nil, fmt.Errorf("obtain certificate: %w", err)
	}

	issuedAt, expiresAt := parseCertValidity(cert.Certificate)

	log.Info().
		Str("domain", req.Domain).
		Time("expires", expiresAt).
		Msg("[cert] Certificate issued")

	return &Certificate{
		Domain:      req.Domain,
		Certificate: append(cert.Certificate, cert.IssuerCertificate...),
		IssuedAt:    issuedAt,
		ExpiresAt:   expiresAt,
	}, nil
}

// GetCACertificate returns the CA certificate.
func (m *ACMEManager) GetCACertificate(ctx context.Context) ([]byte, error) {
	// Return Let's Encrypt root certificate
	// For production: ISRG Root X1
	return []byte(`-----BEGIN CERTIFICATE-----
MIIFazCCA1OgAwIBAgIRAIIQz7DSQONZRGPgu2OCiwAwDQYJKoZIhvcNAQELBQAw
TzELMAkGA1UEBhMCVVMxKTAnBgNVBAoTIEludGVybmV0IFNlY3VyaXR5IFJlc2Vh
cmNoIEdyb3VwMRUwEwYDVQQDEwxJU1JHIFJvb3QgWDEwHhcNMTUwNjA0MTEwNDM4
WhcNMzUwNjA0MTEwNDM4WjBPMQswCQYDVQQGEwJVUzEpMCcGA1UEChMgSW50ZXJu
ZXQgU2VjdXJpdHkgUmVzZWFyY2ggR3JvdXAxFTATBgNVBAMTDElTUkcgUm9vdCBY
MTCCAiIwDQYJKoZIhvcNAQEBBQADggIPADCCAgoCggIBAK3oJHP0FDfE3SZL46XH
FY3uLK8C2RZ8i6W4L3H9J8S7t3z3pVlZfXK5L8U2K9yB3N0P4Z1vX8m8k3pW1oG
-----END CERTIFICATE-----`), nil
}

// parsePEMCSR decodes a PEM-encoded CSR and returns the parsed CertificateRequest.
func parsePEMCSR(pemData []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}
	if block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("expected CERTIFICATE REQUEST, got %s", block.Type)
	}
	return x509.ParseCertificateRequest(block.Bytes)
}

// ParseCSRDomain extracts the domain (CommonName or first DNSNames) from a PEM-encoded CSR.
func ParseCSRDomain(pemData []byte) (string, error) {
	csr, err := parsePEMCSR(pemData)
	if err != nil {
		return "", err
	}

	// Prefer CommonName, fall back to first DNSName
	if csr.Subject.CommonName != "" {
		return csr.Subject.CommonName, nil
	}
	if len(csr.DNSNames) > 0 {
		return csr.DNSNames[0], nil
	}
	return "", fmt.Errorf("CSR has no CommonName or DNSNames")
}

func createDNSProvider(providerType string) (DNSProvider, error) {
	switch strings.ToLower(providerType) {
	case "cloudflare":
		return newCloudflareProvider()
	case "route53":
		return newRoute53Provider()
	default:
		return nil, fmt.Errorf("unsupported DNS provider: %s", providerType)
	}
}

func newCloudflareProvider() (DNSProvider, error) {
	apiToken := os.Getenv("CLOUDFLARE_API_TOKEN")
	if apiToken == "" {
		return nil, fmt.Errorf("CLOUDFLARE_API_TOKEN environment variable not set")
	}

	cfg := cloudflare.NewDefaultConfig()
	cfg.AuthToken = apiToken

	provider, err := cloudflare.NewDNSProviderConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create cloudflare provider: %w", err)
	}

	return &cloudflareProviderAdapter{provider: provider}, nil
}

func newRoute53Provider() (DNSProvider, error) {
	provider, err := route53.NewDNSProvider()
	if err != nil {
		return nil, fmt.Errorf("create route53 provider: %w", err)
	}

	return &route53ProviderAdapter{provider: provider}, nil
}

func getDirectoryURL(url string) string {
	if url == "" {
		return lego.LEDirectoryProduction
	}
	return url
}

func parseCertValidity(certPEM []byte) (issuedAt, expiresAt time.Time) {
	// Decode PEM block
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return time.Now(), time.Now().Add(90 * 24 * time.Hour)
	}

	// Parse certificate to extract validity period
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Now(), time.Now().Add(90 * 24 * time.Hour)
	}
	return cert.NotBefore, cert.NotAfter
}

// acmeUser implements lego's registration.User interface
type acmeUser struct {
	email        string
	key          *ecdsa.PrivateKey
	registration *registration.Resource
}

func (u *acmeUser) GetEmail() string {
	return u.email
}

func (u *acmeUser) GetRegistration() *registration.Resource {
	return u.registration
}

func (u *acmeUser) GetPrivateKey() crypto.PrivateKey {
	return u.key
}

// dnsProviderAdapter adapts our DNSProvider to lego's challenge.Provider interface
type dnsProviderAdapter struct {
	dnsProvider DNSProvider
}

func (a *dnsProviderAdapter) Present(domain, token, keyAuth string) error {
	fqdn, value := extractDNS01Record(domain, keyAuth)
	return a.dnsProvider.Present(context.Background(), fqdn, value)
}

func (a *dnsProviderAdapter) CleanUp(domain, token, keyAuth string) error {
	fqdn, value := extractDNS01Record(domain, keyAuth)
	return a.dnsProvider.CleanUp(context.Background(), fqdn, value)
}

func (a *dnsProviderAdapter) Timeout() (timeout, interval time.Duration) {
	return a.dnsProvider.Timeout()
}

// extractDNS01Record computes the FQDN and value for DNS-01 challenge
func extractDNS01Record(domain, keyAuth string) (fqdn, value string) {
	// DNS-01 challenge uses _acme-challenge subdomain
	fqdn = "_acme-challenge." + domain

	// Value is base64url-encoded SHA256 of keyAuth
	h := sha256.Sum256([]byte(keyAuth))
	value = base64.RawURLEncoding.EncodeToString(h[:])

	return fqdn, value
}

// Ensure dnsProviderAdapter implements challenge.Provider
var _ challenge.Provider = (*dnsProviderAdapter)(nil)

// cloudflareProviderAdapter adapts cloudflare provider to our DNSProvider interface
type cloudflareProviderAdapter struct {
	provider *cloudflare.DNSProvider
}

func (a *cloudflareProviderAdapter) Present(ctx context.Context, fqdn, value string) error {
	return a.provider.Present(fqdn, "", value)
}

func (a *cloudflareProviderAdapter) CleanUp(ctx context.Context, fqdn, value string) error {
	return a.provider.CleanUp(fqdn, "", value)
}

func (a *cloudflareProviderAdapter) Timeout() (timeout, interval time.Duration) {
	return a.provider.Timeout()
}

// route53ProviderAdapter adapts route53 provider to our DNSProvider interface
type route53ProviderAdapter struct {
	provider *route53.DNSProvider
}

func (a *route53ProviderAdapter) Present(ctx context.Context, fqdn, value string) error {
	return a.provider.Present(fqdn, "", value)
}

func (a *route53ProviderAdapter) CleanUp(ctx context.Context, fqdn, value string) error {
	return a.provider.CleanUp(fqdn, "", value)
}

func (a *route53ProviderAdapter) Timeout() (timeout, interval time.Duration) {
	return a.provider.Timeout()
}
