package controlplane

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIssueIdentity(t *testing.T) {
	t.Parallel()

	identity, err := IssueIdentity("lease-identity")
	if err != nil {
		t.Fatalf("IssueIdentity returned error: %v", err)
	}
	if len(identity.Certificate) == 0 {
		t.Fatal("identity certificate chain is empty")
	}

	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		t.Fatalf("parse issued certificate: %v", err)
	}
	if got := strings.TrimSpace(leaf.Subject.CommonName); got != ControlPlaneCertCNPrefix+"lease-identity" {
		t.Fatalf("certificate common name = %q, want %q", got, ControlPlaneCertCNPrefix+"lease-identity")
	}
	if len(leaf.URIs) == 0 || leaf.URIs[0].String() != ControlPlaneLeaseURIPrefix+"lease-identity" {
		t.Fatalf("certificate lease URI = %v, want %q", leaf.URIs, ControlPlaneLeaseURIPrefix+"lease-identity")
	}
	if time.Now().Before(leaf.NotBefore) || time.Now().After(leaf.NotAfter) {
		t.Fatalf("issued certificate validity window does not include current time")
	}
	if leaf.PublicKeyAlgorithm != x509.Ed25519 {
		t.Fatalf("public key algorithm = %v, want %v", leaf.PublicKeyAlgorithm, x509.Ed25519)
	}
	if _, ok := identity.PrivateKey.(ed25519.PrivateKey); !ok {
		t.Fatalf("private key type = %T, want ed25519.PrivateKey", identity.PrivateKey)
	}
}

func TestIssueIdentityRejectsEmptyLeaseID(t *testing.T) {
	t.Parallel()

	if _, err := IssueIdentity(" "); err == nil {
		t.Fatal("expected error for empty lease ID")
	}
}

func TestIssueIdentityWithPolicyRejectsInvalidTTL(t *testing.T) {
	t.Parallel()

	if _, err := IssueIdentityWithPolicy("lease-identity", IssuePolicy{
		Backdate: DefaultIdentityBackdate,
		TTL:      0,
	}); err == nil {
		t.Fatal("expected error for invalid ttl")
	}
}

func TestValidatePeerLeaseCertificate(t *testing.T) {
	t.Parallel()

	identity, err := IssueIdentity("lease-identity")
	if err != nil {
		t.Fatalf("IssueIdentity returned error: %v", err)
	}
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		t.Fatalf("parse issued certificate: %v", err)
	}

	state := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leaf},
	}
	if code, msg, ok := ValidatePeerLeaseCertificate(state, "lease-identity"); !ok {
		t.Fatalf("ValidatePeerLeaseCertificate failed: code=%s msg=%s", code, msg)
	}
}

func TestValidatePeerLeaseCertificateRequiresClientAuthEKU(t *testing.T) {
	t.Parallel()

	leaseURI, err := url.Parse(ControlPlaneLeaseURIPrefix + "lease-identity")
	if err != nil {
		t.Fatalf("parse lease uri: %v", err)
	}
	state := &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{
			{
				NotBefore: time.Now().Add(-1 * time.Minute),
				NotAfter:  time.Now().Add(1 * time.Minute),
				Subject: pkix.Name{
					CommonName: ControlPlaneCertCNPrefix + "lease-identity",
				},
				URIs: []*url.URL{leaseURI},
			},
		},
	}

	if code, _, ok := ValidatePeerLeaseCertificate(state, "lease-identity"); ok || code != "client_cert_invalid" {
		t.Fatalf("expected client_cert_invalid for missing EKU, got code=%s ok=%v", code, ok)
	}
}

func TestValidatePeerLeaseCertificateRejectsMissingLeaseID(t *testing.T) {
	t.Parallel()

	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{mustIssuedLeafCertificate(t, "lease-identity")}}
	if code, _, ok := ValidatePeerLeaseCertificate(state, " \t "); ok || code != "missing_lease_id" {
		t.Fatalf("expected missing_lease_id, got code=%s ok=%v", code, ok)
	}
}

func TestValidatePeerLeaseCertificateRequiresPeerCertificates(t *testing.T) {
	t.Parallel()

	if code, _, ok := ValidatePeerLeaseCertificate(nil, "lease-identity"); ok || code != "client_cert_required" {
		t.Fatalf("expected client_cert_required, got code=%s ok=%v", code, ok)
	}
}

func TestValidatePeerLeaseCertificateRejectsCertificateOutsideValidityWindow(t *testing.T) {
	t.Parallel()

	leaf := *mustIssuedLeafCertificate(t, "lease-identity")
	leaf.NotBefore = time.Now().Add(-2 * time.Minute)
	leaf.NotAfter = time.Now().Add(-1 * time.Minute)
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{&leaf}}
	if code, _, ok := ValidatePeerLeaseCertificate(state, "lease-identity"); ok || code != "client_cert_invalid" {
		t.Fatalf("expected client_cert_invalid for expired certificate, got code=%s ok=%v", code, ok)
	}
}

func TestValidatePeerLeaseCertificateRejectsNonClientAuthEKU(t *testing.T) {
	t.Parallel()

	leaf := *mustIssuedLeafCertificate(t, "lease-identity")
	leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{&leaf}}
	if code, _, ok := ValidatePeerLeaseCertificate(state, "lease-identity"); ok || code != "client_cert_invalid" {
		t.Fatalf("expected client_cert_invalid for non-client-auth EKU, got code=%s ok=%v", code, ok)
	}
}

func TestValidatePeerLeaseCertificateRejectsMissingLeaseIdentity(t *testing.T) {
	t.Parallel()

	leaf := *mustIssuedLeafCertificate(t, "lease-identity")
	leaf.Subject = pkix.Name{CommonName: "unrelated"}
	leaf.URIs = nil
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{&leaf}}
	if code, _, ok := ValidatePeerLeaseCertificate(state, "lease-identity"); ok || code != "cert_lease_missing" {
		t.Fatalf("expected cert_lease_missing, got code=%s ok=%v", code, ok)
	}
}

func TestValidatePeerLeaseCertificateRejectsLeaseIDMismatch(t *testing.T) {
	t.Parallel()

	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{mustIssuedLeafCertificate(t, "lease-identity")}}
	if code, _, ok := ValidatePeerLeaseCertificate(state, "different-lease"); ok || code != "cert_lease_mismatch" {
		t.Fatalf("expected cert_lease_mismatch, got code=%s ok=%v", code, ok)
	}
}

func TestExtractLeaseIDFromPeerCertificateRejectsUnprefixedCN(t *testing.T) {
	t.Parallel()

	leaseID := ExtractLeaseIDFromPeerCertificate(&x509.Certificate{
		Subject: pkix.Name{CommonName: "lease-identity"},
	})
	if leaseID != "" {
		t.Fatalf("expected empty lease id for unprefixed CN, got %q", leaseID)
	}
}

func mustIssuedLeafCertificate(t *testing.T, leaseID string) *x509.Certificate {
	t.Helper()

	identity, err := IssueIdentity(leaseID)
	if err != nil {
		t.Fatalf("IssueIdentity returned error: %v", err)
	}
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		t.Fatalf("parse issued certificate: %v", err)
	}
	return leaf
}
