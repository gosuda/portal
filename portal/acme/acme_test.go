package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	certPEM, keyPEM, err := manager.EnsureTLSMaterial(context.Background())
	if err != nil {
		t.Fatalf("EnsureTLSMaterial() error = %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Fatalf("EnsureTLSMaterial() returned empty PEM material")
	}

	certFile, _, err := manager.TLSFiles()
	if err != nil {
		t.Fatalf("TLSFiles() error = %v", err)
	}
	covered, err := certCoversDomains(certFile, []string{"localhost"})
	if err != nil {
		t.Fatalf("certCoversDomains() error = %v", err)
	}
	if !covered {
		t.Fatal("certCoversDomains() = false, want true")
	}
}

func TestEnsureTLSMaterialUsesManualCertificateWithoutDNSProvider(t *testing.T) {
	t.Parallel()

	keyDir := t.TempDir()
	if err := writeManualRelayCertificate(t, keyDir, "portal.example.com"); err != nil {
		t.Fatalf("writeManualRelayCertificate() error = %v", err)
	}

	manager, err := NewManager(Config{
		BaseDomain: "portal.example.com",
		KeyDir:     keyDir,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	certPEM, keyPEM, err := manager.EnsureTLSMaterial(context.Background())
	if err != nil {
		t.Fatalf("EnsureTLSMaterial() error = %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Fatalf("EnsureTLSMaterial() returned empty PEM material")
	}
}

func TestEnsureTLSMaterialRequiresManualCertificateWhenProviderUnset(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(Config{
		BaseDomain: "portal.example.com",
		KeyDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	_, _, err = manager.EnsureTLSMaterial(context.Background())
	if err == nil {
		t.Fatal("EnsureTLSMaterial() error = nil, want missing manual certificate error")
	}
	if got := err.Error(); got == "" || !containsAll(got, "manual certificate mode requires", "fullchain.pem", "privatekey.pem") {
		t.Fatalf("EnsureTLSMaterial() error = %q, want manual certificate guidance", got)
	}
}

func TestNewManagerRejectsENSGaslessWithoutDNSProvider(t *testing.T) {
	t.Parallel()

	_, err := NewManager(Config{
		BaseDomain:        "portal.example.com",
		KeyDir:            t.TempDir(),
		ENSGaslessEnabled: true,
		ENSGaslessAddress: "0x1234567890123456789012345678901234567890",
	})
	if err == nil {
		t.Fatal("NewManager() error = nil, want ENS gasless provider error")
	}
	if got := err.Error(); got != "ens gasless automation requires ACME_DNS_PROVIDER" {
		t.Fatalf("NewManager() error = %q, want ENS gasless provider guidance", got)
	}
}

func TestEnsureTLSMaterialUsesManualCertificateWithDNSProvider(t *testing.T) {
	t.Parallel()

	keyDir := t.TempDir()
	if err := writeManualRelayCertificate(t, keyDir, "portal.example.com"); err != nil {
		t.Fatalf("writeManualRelayCertificate() error = %v", err)
	}

	manager, err := NewManager(Config{
		BaseDomain:  "portal.example.com",
		KeyDir:      keyDir,
		DNSProvider: TypeRoute53,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	certPEM, keyPEM, err := manager.EnsureTLSMaterial(context.Background())
	if err != nil {
		t.Fatalf("EnsureTLSMaterial() error = %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		t.Fatalf("EnsureTLSMaterial() returned empty PEM material")
	}
}

func writeManualRelayCertificate(t *testing.T, keyDir, baseDomain string) error {
	t.Helper()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject: pkix.Name{
			CommonName: baseDomain,
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(90 * 24 * time.Hour),
		DNSNames:              []string{baseDomain, "*." + baseDomain},
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, privateKey.Public(), privateKey)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(filepath.Join(keyDir, fullChainFileName), certPEM, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(keyDir, keyFileName), keyPEM, 0o600)
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
