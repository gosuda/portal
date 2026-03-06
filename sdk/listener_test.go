package sdk

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"gosuda.org/portal/internal/testutil"
	"gosuda.org/portal/portal"
)

func TestListenerEndToEndTLSHTTP(t *testing.T) {
	t.Parallel()

	apiCertPEM, apiKeyPEM, err := testutil.SelfSignedCertPEM("127.0.0.1")
	if err != nil {
		t.Fatalf("SelfSignedCertPEM(api) error = %v", err)
	}
	tenantHost := "app.portal.test"
	tenantCertPEM, tenantKeyPEM, err := testutil.SelfSignedCertPEM(tenantHost)
	if err != nil {
		t.Fatalf("SelfSignedCertPEM(tenant) error = %v", err)
	}

	relay, err := portal.NewServer(portal.ServerConfig{
		PortalURL:        "https://127.0.0.1",
		APIListenAddr:    "127.0.0.1:0",
		SNIListenAddr:    "127.0.0.1:0",
		RootHost:         "portal.test",
		RootFallbackAddr: "127.0.0.1:1",
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
	if err := relay.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
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
		TLS: portal.TLSMaterialConfig{
			CertPEM: tenantCertPEM,
			KeyPEM:  tenantKeyPEM,
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
			if err != nil && err != http.ErrServerClosed && !errors.Is(err, net.ErrClosed) {
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

func TestListenerEndToEndTLSHTTP_AutoSelfSigned(t *testing.T) {
	t.Parallel()

	apiCertPEM, apiKeyPEM, err := testutil.SelfSignedCertPEM("127.0.0.1")
	if err != nil {
		t.Fatalf("SelfSignedCertPEM(api) error = %v", err)
	}
	tenantHost := "auto.portal.test"

	relay, err := portal.NewServer(portal.ServerConfig{
		PortalURL:        "https://127.0.0.1",
		APIListenAddr:    "127.0.0.1:0",
		SNIListenAddr:    "127.0.0.1:0",
		RootHost:         "portal.test",
		RootFallbackAddr: "127.0.0.1:1",
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
	if err := relay.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
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
			_, _ = io.WriteString(w, "auto tls ok\n")
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
			if err != nil && err != http.ErrServerClosed && !errors.Is(err, net.ErrClosed) {
				t.Fatalf("server.Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		body, err := doTenantRequest(relay.SNIAddr(), tenantHost, "/")
		if err == nil {
			if !strings.Contains(body, "auto tls ok") {
				t.Fatalf("body = %q, want auto tls payload", body)
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

	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, host); err != nil {
		return "", err
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
