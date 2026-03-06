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

	client, err := NewClient(ClientConfig{RelayURL: server.URL})
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
