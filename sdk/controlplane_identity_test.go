package sdk

import (
	"crypto/x509"
	"strings"
	"testing"
	"time"
)

func TestIssueControlPlaneIdentity(t *testing.T) {
	t.Parallel()

	identity, err := issueControlPlaneIdentity("lease-identity")
	if err != nil {
		t.Fatalf("issueControlPlaneIdentity returned error: %v", err)
	}
	if len(identity.Certificate) == 0 {
		t.Fatal("identity certificate chain is empty")
	}

	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		t.Fatalf("parse issued certificate: %v", err)
	}
	if got := strings.TrimSpace(leaf.Subject.CommonName); got != controlPlaneCertCNPrefix+"lease-identity" {
		t.Fatalf("certificate common name = %q, want %q", got, controlPlaneCertCNPrefix+"lease-identity")
	}
	if len(leaf.URIs) == 0 || leaf.URIs[0].String() != controlPlaneLeaseURIPfx+"lease-identity" {
		t.Fatalf("certificate lease URI = %v, want %q", leaf.URIs, controlPlaneLeaseURIPfx+"lease-identity")
	}
	if time.Now().Before(leaf.NotBefore) || time.Now().After(leaf.NotAfter) {
		t.Fatalf("issued certificate validity window does not include current time")
	}
}

func TestIssueControlPlaneIdentityRejectsEmptyLeaseID(t *testing.T) {
	t.Parallel()

	if _, err := issueControlPlaneIdentity(" "); err == nil {
		t.Fatal("expected error for empty lease ID")
	}
}
