package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"slices"
	"testing"
	"time"
)

func TestGenerateSelfSignedCert_BasicSuccess(t *testing.T) {
	tlsCert, hash, err := generateSelfSignedCert()
	if err != nil {
		t.Fatalf("generateSelfSignedCert() error = %v", err)
	}

	if len(tlsCert.Certificate) == 0 || len(tlsCert.Certificate[0]) == 0 {
		t.Fatal("expected non-empty tls.Certificate DER chain")
	}
	if len(hash) != sha256.Size {
		t.Fatalf("hash length = %d, want %d", len(hash), sha256.Size)
	}
}

func TestGenerateSelfSignedCert_HashMatchesDER(t *testing.T) {
	tlsCert, hash, err := generateSelfSignedCert()
	if err != nil {
		t.Fatalf("generateSelfSignedCert() error = %v", err)
	}
	if len(tlsCert.Certificate) == 0 {
		t.Fatal("expected certificate chain to be non-empty")
	}

	sum := sha256.Sum256(tlsCert.Certificate[0])
	if !bytes.Equal(hash, sum[:]) {
		t.Fatalf("hash mismatch: got %x want %x", hash, sum)
	}
}

func TestGenerateSelfSignedCert_X509Properties(t *testing.T) {
	tlsCert, _, err := generateSelfSignedCert()
	if err != nil {
		t.Fatalf("generateSelfSignedCert() error = %v", err)
	}
	if len(tlsCert.Certificate) == 0 {
		t.Fatal("expected certificate chain to be non-empty")
	}

	cert, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		t.Fatalf("x509.ParseCertificate() error = %v", err)
	}

	if cert.Subject.CommonName != "portal-dev" {
		t.Fatalf("subject common name = %q, want %q", cert.Subject.CommonName, "portal-dev")
	}
	if !slices.Contains(cert.DNSNames, "localhost") {
		t.Fatalf("DNSNames = %v, want to contain %q", cert.DNSNames, "localhost")
	}
	if cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Fatalf("KeyUsage = %v, want DigitalSignature bit set", cert.KeyUsage)
	}
	if !slices.Contains(cert.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		t.Fatalf("ExtKeyUsage = %v, want to contain ServerAuth", cert.ExtKeyUsage)
	}

	validity := cert.NotAfter.Sub(cert.NotBefore)
	if validity >= 14*24*time.Hour {
		t.Fatalf("validity duration = %v, want < %v", validity, 14*24*time.Hour)
	}
	if validity <= 10*24*time.Hour {
		t.Fatalf("validity duration = %v, want > %v", validity, 10*24*time.Hour)
	}

	now := time.Now()
	const skew = 2 * time.Minute
	if cert.NotBefore.After(now.Add(skew)) {
		t.Fatalf("NotBefore = %v, now = %v, skew = %v", cert.NotBefore, now, skew)
	}
	if cert.NotAfter.Before(now.Add(-skew)) {
		t.Fatalf("NotAfter = %v, now = %v, skew = %v", cert.NotAfter, now, skew)
	}
}
