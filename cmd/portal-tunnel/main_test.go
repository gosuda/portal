package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchConsistentCertHashAutoSingleRelaySuccess(t *testing.T) {
	t.Parallel()

	srv, expectedHash := newCertHashTLSServer(t)

	hash, err := fetchConsistentCertHash(context.Background(), []string{relayURL(srv, "/relay")})
	if err != nil {
		t.Fatalf("fetchConsistentCertHash() error = %v", err)
	}

	if got := hex.EncodeToString(hash); got != expectedHash {
		t.Fatalf("fetchConsistentCertHash() hash = %s, want %s", got, expectedHash)
	}
}

func TestFetchConsistentCertHashAutoMultiRelaySameCertSuccess(t *testing.T) {
	t.Parallel()

	srv1, expectedHash := newCertHashTLSServer(t)
	cert := srv1.TLS.Certificates[0]
	srv2 := newCertHashTLSServerWithCertificate(t, cert, expectedHash)

	hash, err := fetchConsistentCertHash(
		context.Background(),
		[]string{relayURL(srv1, "/relay-a"), relayURL(srv2, "/relay-b")},
	)
	if err != nil {
		t.Fatalf("fetchConsistentCertHash() error = %v", err)
	}

	if got := hex.EncodeToString(hash); got != expectedHash {
		t.Fatalf("fetchConsistentCertHash() hash = %s, want %s", got, expectedHash)
	}
}

func TestFetchConsistentCertHashAutoMultiRelayDifferentCertFails(t *testing.T) {
	t.Parallel()

	cert1 := newSelfSignedTLSCertificate(t, "relay-a")
	hash1 := certificateHashHex(t, cert1)
	srv1 := newCertHashTLSServerWithCertificate(t, cert1, hash1)

	cert2 := newSelfSignedTLSCertificate(t, "relay-b")
	hash2 := certificateHashHex(t, cert2)
	srv2 := newCertHashTLSServerWithCertificate(t, cert2, hash2)

	if hash1 == hash2 {
		t.Fatal("expected different hashes from distinct certificates")
	}

	relay1 := relayURL(srv1, "/relay-a")
	relay2 := relayURL(srv2, "/relay-b")

	_, err := fetchConsistentCertHash(context.Background(), []string{relay1, relay2})
	if err == nil {
		t.Fatal("fetchConsistentCertHash() expected mismatch error, got nil")
	}

	errMsg := err.Error()
	for _, want := range []string{
		"relay certificate hash mismatch",
		relay1,
		relay2,
		hash1,
		hash2,
	} {
		if !strings.Contains(errMsg, want) {
			t.Fatalf("mismatch error %q does not include %q", errMsg, want)
		}
	}
}

func TestFetchCertHashFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		body      string
		wantError string
	}{
		{
			name:      "non-200 status",
			status:    http.StatusBadGateway,
			body:      `{"error":"upstream unavailable"}`,
			wantError: "cert-hash endpoint returned status 502",
		},
		{
			name:      "malformed json payload",
			status:    http.StatusOK,
			body:      `{"algorithm":"sha-256","hash":`,
			wantError: "failed to decode cert hash response",
		},
		{
			name:      "malformed hash payload",
			status:    http.StatusOK,
			body:      `{"algorithm":"sha-256","hash":"zz"}`,
			wantError: "invalid cert hash hex",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := newFixedCertHashResponseTLSServer(t, tc.status, tc.body)

			_, err := fetchCertHash(context.Background(), relayURL(srv, "/relay"))
			if err == nil {
				t.Fatal("fetchCertHash() expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("fetchCertHash() error = %q, want contains %q", err.Error(), tc.wantError)
			}
		})
	}
}

func newCertHashTLSServer(t *testing.T) (srv *httptest.Server, hash string) {
	t.Helper()

	var responseBody string
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cert-hash" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(srv.Close)

	hash = certificateHashHex(t, srv.TLS.Certificates[0])
	responseBody = fmt.Sprintf(`{"algorithm":"sha-256","hash":%q}`, hash)

	return srv, hash
}

func newCertHashTLSServerWithCertificate(t *testing.T, cert tls.Certificate, hash string) *httptest.Server {
	t.Helper()

	responseBody := fmt.Sprintf(`{"algorithm":"sha-256","hash":%q}`, hash)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cert-hash" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return srv
}

func newFixedCertHashResponseTLSServer(t *testing.T, statusCode int, body string) *httptest.Server {
	t.Helper()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cert-hash" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(statusCode)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv
}

func certificateHashHex(t *testing.T, cert tls.Certificate) string {
	t.Helper()

	if len(cert.Certificate) == 0 {
		t.Fatal("certificate chain is empty")
	}
	sum := sha256.Sum256(cert.Certificate[0])

	return hex.EncodeToString(sum[:])
}

func relayURL(srv *httptest.Server, suffix string) string {
	return srv.URL + suffix
}

func newSelfSignedTLSCertificate(t *testing.T, commonName string) tls.Certificate {
	t.Helper()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey() error = %v", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 62)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		t.Fatalf("rand.Int() error = %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: commonName,
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, privateKey.Public(), privateKey)
	if err != nil {
		t.Fatalf("x509.CreateCertificate() error = %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("x509.MarshalECPrivateKey() error = %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("tls.X509KeyPair() error = %v", err)
	}

	return cert
}
