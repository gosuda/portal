package acme

import (
	"context"
	"testing"
)

func TestEnsureCertificateGeneratesLocalDevelopmentMaterial(t *testing.T) {
	t.Parallel()

	keyDir := t.TempDir()
	manager, err := NewManager(Config{
		BaseDomain: "localhost",
		KeyDir:     keyDir,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	certFile, keyFile, err := manager.EnsureCertificate(context.Background())
	if err != nil {
		t.Fatalf("EnsureCertificate() error = %v", err)
	}
	if certFile == "" || keyFile == "" {
		t.Fatalf("EnsureCertificate() = %q, %q, want certificate paths", certFile, keyFile)
	}

	covered, err := certCoversDomains(certFile, []string{"localhost"})
	if err != nil {
		t.Fatalf("certCoversDomains() error = %v", err)
	}
	if !covered {
		t.Fatal("certCoversDomains() = false, want true")
	}
}
