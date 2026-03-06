package sdk

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"gosuda.org/portal/portal"
	portalkeyless "gosuda.org/portal/portal/keyless"
)

func selfSignedCertPEM(hosts ...string) (certPEM, keyPEM []byte, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: hosts[0],
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
			continue
		}
		template.DNSNames = append(template.DNSNames, host)
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	return certPEM, keyPEM, nil
}

func TestListenerEndToEndTLSHTTP(t *testing.T) {
	t.Parallel()

	apiCertPEM, apiKeyPEM, err := selfSignedCertPEM("127.0.0.1", "portal.test", "*.portal.test")
	if err != nil {
		t.Fatalf("selfSignedCertPEM(api) error = %v", err)
	}
	tenantHost := "app.portal.test"

	relay, err := portal.NewServer(portal.ServerConfig{
		PortalURL:            "https://127.0.0.1",
		APIListenAddr:        "127.0.0.1:0",
		SNIListenAddr:        "127.0.0.1:0",
		RootHost:             "portal.test",
		RootFallbackAddr:     "127.0.0.1:1",
		KeylessSignerHandler: newTestSignerHandler(t, apiKeyPEM),
		APITLS: portal.TLSMaterialConfig{
			CertPEM: apiCertPEM,
			KeyPEM:  apiKeyPEM,
		},
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startErr := relay.Start(ctx)
	if startErr != nil {
		t.Fatalf("Start() error = %v", startErr)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = relay.Shutdown(shutdownCtx)
		_ = relay.Wait()
	})

	client, err := NewClient(ClientConfig{
		RelayURL:           "https://" + relay.APIAddr(),
		InsecureSkipVerify: true,
		ReadyTarget:        1,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(client.Close)

	listener, err := client.Listen(ctx, ListenRequest{
		Name:      "demo",
		Hostnames: []string{tenantHost},
		Metadata: LeaseMetadata{
			Description: "demo description",
			Tags:        []string{"demo", "test", "demo"},
			Owner:       "portal",
			Thumbnail:   "https://example.test/thumb.png",
			Hide:        true,
		},
	})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	if listener.Metadata().Description != "demo description" {
		t.Fatalf("listener.Metadata().Description = %q", listener.Metadata().Description)
	}
	if got, ok := relay.GetLease(listener.LeaseID()); !ok {
		t.Fatalf("GetLease(%q) = not found", listener.LeaseID())
	} else {
		if got.Metadata.Owner != "portal" {
			t.Fatalf("GetLease().Metadata.Owner = %q", got.Metadata.Owner)
		}
		if len(got.Metadata.Tags) != 2 {
			t.Fatalf("GetLease().Metadata.Tags = %v, want deduped tags", got.Metadata.Tags)
		}
		if !got.Metadata.Hide {
			t.Fatal("GetLease().Metadata.Hide = false, want true")
		}
	}

	httpDone := make(chan error, 1)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "hello over relay\n")
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		httpDone <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		select {
		case err := <-httpDone:
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("server.Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		body, err := doTenantRequest(relay.SNIAddr(), tenantHost, "/")
		if err == nil {
			if !strings.Contains(body, "hello over relay") {
				t.Fatalf("body = %q, want relay payload", body)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("doTenantRequest() last error = %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestListenerEndToEndTLSHTTP_AutoKeyless(t *testing.T) {
	t.Parallel()

	apiCertPEM, apiKeyPEM, err := selfSignedCertPEM("127.0.0.1", "portal.test", "*.portal.test")
	if err != nil {
		t.Fatalf("selfSignedCertPEM(api) error = %v", err)
	}
	tenantHost := "auto.portal.test"
	relay, err := portal.NewServer(portal.ServerConfig{
		PortalURL:            "https://127.0.0.1",
		APIListenAddr:        "127.0.0.1:0",
		SNIListenAddr:        "127.0.0.1:0",
		RootHost:             "portal.test",
		RootFallbackAddr:     "127.0.0.1:1",
		KeylessSignerHandler: newTestSignerHandler(t, apiKeyPEM),
		APITLS: portal.TLSMaterialConfig{
			CertPEM: apiCertPEM,
			KeyPEM:  apiKeyPEM,
		},
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startErr := relay.Start(ctx)
	if startErr != nil {
		t.Fatalf("Start() error = %v", startErr)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = relay.Shutdown(shutdownCtx)
		_ = relay.Wait()
	})

	client, err := NewClient(ClientConfig{
		RelayURL:           "https://" + relay.APIAddr(),
		InsecureSkipVerify: true,
		ReadyTarget:        1,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	t.Cleanup(client.Close)

	listener, err := client.Listen(ctx, ListenRequest{
		Name:      "auto-demo",
		Hostnames: []string{tenantHost},
	})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	httpDone := make(chan error, 1)
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "auto keyless ok\n")
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		httpDone <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		select {
		case err := <-httpDone:
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("server.Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		body, err := doTenantRequest(relay.SNIAddr(), tenantHost, "/")
		if err == nil {
			if !strings.Contains(body, "auto keyless ok") {
				t.Fatalf("body = %q, want auto keyless payload", body)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("doTenantRequest() last error = %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func doTenantRequest(addr, host, path string) (string, error) {
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true,
		NextProtos:         []string{"http/1.1"},
	})
	if err != nil {
		return "", err
	}
	defer conn.Close()

	if _, writeErr := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, host); writeErr != nil {
		return "", writeErr
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return string(body), nil
}

func newTestSignerHandler(t *testing.T, keyPEM []byte) http.Handler {
	t.Helper()

	keyFile := t.TempDir() + "/relay-key.pem"
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("WriteFile(key) error = %v", err)
	}
	signer, err := portalkeyless.NewSigner(keyFile)
	if err != nil {
		t.Fatalf("NewSigner() error = %v", err)
	}
	return signer.Handler()
}
