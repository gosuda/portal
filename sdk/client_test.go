package sdk

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
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

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	resp, err := client.httpClient.Get(server.URL)
	if err != nil {
		t.Fatalf("httpClient.Get() error = %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestNewClientAppliesOptions(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	rootCAPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: server.Certificate().Raw,
	})
	client, err := NewClient(
		"https://relay.example.com/base/",
		WithRootCAPEM(rootCAPEM),
		WithInsecureSkipVerify(true),
		WithDialTimeout(2*time.Second),
		WithRequestTimeout(3*time.Second),
		WithHandshakeTimeout(4*time.Second),
		WithLeaseTTL(5*time.Minute),
		WithRenewBefore(45*time.Second),
		WithReadyTarget(3),
	)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()

	if got := client.baseURL.String(); got != "https://relay.example.com/base" {
		t.Fatalf("baseURL.String() = %q, want %q", got, "https://relay.example.com/base")
	}
	if !client.insecureSkipVerify {
		t.Fatal("insecureSkipVerify = false, want true")
	}
	if client.dialTimeout != 2*time.Second {
		t.Fatalf("dialTimeout = %v, want %v", client.dialTimeout, 2*time.Second)
	}
	if client.requestTimeout != 3*time.Second {
		t.Fatalf("requestTimeout = %v, want %v", client.requestTimeout, 3*time.Second)
	}
	if client.handshakeTimeout != 4*time.Second {
		t.Fatalf("handshakeTimeout = %v, want %v", client.handshakeTimeout, 4*time.Second)
	}
	if client.leaseTTL != 5*time.Minute {
		t.Fatalf("leaseTTL = %v, want %v", client.leaseTTL, 5*time.Minute)
	}
	if client.renewBefore != 45*time.Second {
		t.Fatalf("renewBefore = %v, want %v", client.renewBefore, 45*time.Second)
	}
	if client.readyTarget != 3 {
		t.Fatalf("readyTarget = %d, want %d", client.readyTarget, 3)
	}
	if client.httpClient.Timeout != 3*time.Second {
		t.Fatalf("httpClient.Timeout = %v, want %v", client.httpClient.Timeout, 3*time.Second)
	}
	if !client.rawTLSConfig.InsecureSkipVerify {
		t.Fatal("rawTLSConfig.InsecureSkipVerify = false, want true")
	}
	if string(client.rootCAPEM) != string(rootCAPEM) {
		t.Fatalf("rootCAPEM = %q, want copied PEM input", string(client.rootCAPEM))
	}

	rootCAPEM[0] = 'X'
	if string(client.rootCAPEM) == string(rootCAPEM) {
		t.Fatalf("rootCAPEM changed with caller slice mutation: %q", string(client.rootCAPEM))
	}
	if len(client.rootCAPEM) == 0 || client.rootCAPEM[0] != '-' {
		t.Fatalf("rootCAPEM changed with caller slice mutation: %q", string(client.rootCAPEM))
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

	client := &Client{
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
		dialTimeout: defaultDialTimeout,
	}

	_, err = client.openReverseSession(context.Background(), "lease-123", "tok_123")
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
