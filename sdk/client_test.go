package sdk

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
