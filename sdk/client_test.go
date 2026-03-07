package sdk

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/types"
)

func TestNewClientAutoTrustsLocalhostRelayCertificate(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{RelayURLs: []string{server.URL}})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	if len(client.clients) != 1 {
		t.Fatalf("client count = %d, want 1", len(client.clients))
	}

	resp, err := client.clients[0].httpClient.Get(server.URL)
	if err != nil {
		t.Fatalf("httpClient.Get() error = %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestNewClientSupportsDedupedRelayURLs(t *testing.T) {
	t.Parallel()

	serverA := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer serverA.Close()

	serverB := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer serverB.Close()

	client, err := NewClient(ClientConfig{
		RelayURLs: []string{serverA.URL, serverB.URL},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	if len(client.clients) != 2 {
		t.Fatalf("client count = %d, want 2", len(client.clients))
	}

	for i, relayClient := range client.clients {
		resp, err := relayClient.httpClient.Get(relayClient.baseURL.String())
		if err != nil {
			t.Fatalf("client[%d].httpClient.Get() error = %v", i, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("client[%d] status = %d, want %d", i, resp.StatusCode, http.StatusOK)
		}
	}
}

func TestOpenReverseSessionPreservesAPIErrorCode(t *testing.T) {
	t.Parallel()

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != types.PathSDKConnect {
			t.Fatalf("request path = %q, want %q", r.URL.Path, types.PathSDKConnect)
		}
		if got := r.URL.Query().Get("lease_id"); got != "lease-123" {
			t.Fatalf("lease_id = %q, want %q", got, "lease-123")
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(25 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(types.APIEnvelope[any]{
			OK:    false,
			Error: &types.APIError{Code: "unauthorized", Message: "bad reverse token"},
		})
	}))
	server.EnableHTTP2 = false
	server.StartTLS()
	defer server.Close()

	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("server client transport type = %T, want *http.Transport", server.Client().Transport)
	}

	relayClient := &relayClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Transport: transport.Clone(),
			Timeout:   defaultRequestTimeout,
		},
		rawTLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: baseURL.Hostname(),
			RootCAs:    transport.TLSClientConfig.RootCAs,
			NextProtos: []string{"http/1.1"},
		},
	}

	_, err = relayClient.openReverseSession(context.Background(), "lease-123", "tok_123")
	if err == nil {
		t.Fatal("openReverseSession() error = nil, want APIRequestError")
	}

	var apiErr *types.APIRequestError
	if !errors.As(err, &apiErr) {
		t.Fatalf("openReverseSession() error = %T, want *types.APIRequestError", err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		t.Fatalf("APIRequestError.StatusCode = %d, want %d", apiErr.StatusCode, http.StatusForbidden)
	}
	if apiErr.Code != "unauthorized" {
		t.Fatalf("APIRequestError.Code = %q, want %q", apiErr.Code, "unauthorized")
	}
	if apiErr.Message != "bad reverse token" {
		t.Fatalf("APIRequestError.Message = %q, want %q", apiErr.Message, "bad reverse token")
	}
}
