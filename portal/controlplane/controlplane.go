package controlplane

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	// ControlPlaneCertCNPrefix is the CN prefix used for lease-bound client identity certs.
	ControlPlaneCertCNPrefix = "lease:"
	// ControlPlaneLeaseURIPrefix is the URI prefix used in lease-bound SPIFFE-like identities.
	ControlPlaneLeaseURIPrefix = "spiffe://portal/lease/"
	// DefaultIdentityBackdate offsets notBefore to tolerate small clock skew.
	DefaultIdentityBackdate = 1 * time.Minute
	// DefaultIdentityTTL is the default issued identity lifetime.
	DefaultIdentityTTL = 24 * time.Hour
)

// IssuePolicy configures control-plane identity validity windows.
type IssuePolicy struct {
	Backdate time.Duration
	TTL      time.Duration
}

var defaultIssuePolicy = IssuePolicy{
	Backdate: DefaultIdentityBackdate,
	TTL:      DefaultIdentityTTL,
}

// IssueIdentity issues a self-signed lease-bound client identity certificate
// for control-plane mTLS.
func IssueIdentity(leaseID string) (tls.Certificate, error) {
	return IssueIdentityWithPolicy(leaseID, defaultIssuePolicy)
}

// IssueIdentityWithPolicy issues a self-signed lease-bound client identity
// certificate using explicit validity policy.
func IssueIdentityWithPolicy(leaseID string, policy IssuePolicy) (tls.Certificate, error) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return tls.Certificate{}, errors.New("lease id is required")
	}
	if policy.Backdate <= 0 {
		return tls.Certificate{}, errors.New("identity backdate must be greater than zero")
	}
	if policy.TTL <= 0 {
		return tls.Certificate{}, errors.New("identity ttl must be greater than zero")
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate identity key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate serial: %w", err)
	}
	notBefore := time.Now().Add(-policy.Backdate)
	notAfter := notBefore.Add(policy.TTL)

	leaseURI, err := url.Parse(ControlPlaneLeaseURIPrefix + leaseID)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("build lease URI: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: ControlPlaneCertCNPrefix + leaseID,
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{leaseURI},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create lease identity certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal identity key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load identity key pair: %w", err)
	}
	return cert, nil
}

// MatchLeaseToken compares lease-bound values in constant time.
func MatchLeaseToken(expected, provided string) bool {
	expected = strings.TrimSpace(expected)
	provided = strings.TrimSpace(provided)
	if expected == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

// ExtractLeaseIDFromPeerCertificate extracts lease identity from URI SAN first,
// then CN as fallback.
func ExtractLeaseIDFromPeerCertificate(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	for _, uri := range cert.URIs {
		if uri == nil {
			continue
		}
		raw := strings.TrimSpace(uri.String())
		if after, ok := strings.CutPrefix(raw, ControlPlaneLeaseURIPrefix); ok {
			return after
		}
	}

	commonName := strings.TrimSpace(cert.Subject.CommonName)
	if after, ok := strings.CutPrefix(commonName, ControlPlaneCertCNPrefix); ok {
		return after
	}
	return ""
}

// ValidatePeerLeaseCertificate validates lease-bound client certificate
// material from an incoming TLS connection state.
func ValidatePeerLeaseCertificate(state *tls.ConnectionState, leaseID string) (string, string, bool) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return "missing_lease_id", "lease id is required", false
	}
	if state == nil || len(state.PeerCertificates) == 0 {
		return "client_cert_required", "client certificate is required", false
	}

	leaf := state.PeerCertificates[0]
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return "client_cert_invalid", "client certificate is outside validity window", false
	}

	if len(leaf.ExtKeyUsage) == 0 {
		return "client_cert_invalid", "client certificate must include client authentication extended key usage", false
	}
	hasClientAuth := slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
	if !hasClientAuth {
		return "client_cert_invalid", "client certificate does not allow client authentication", false
	}

	certLeaseID := strings.TrimSpace(ExtractLeaseIDFromPeerCertificate(leaf))
	if certLeaseID == "" {
		return "cert_lease_missing", "client certificate does not include lease identity", false
	}
	if !MatchLeaseToken(leaseID, certLeaseID) {
		return "cert_lease_mismatch", fmt.Sprintf("client certificate lease identity mismatch: requested=%s cert=%s", leaseID, certLeaseID), false
	}
	return "", "", true
}
