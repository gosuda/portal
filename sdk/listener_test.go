package sdk

import (
	"strings"
	"testing"
)

func TestNormalizeRelayAPIURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "localhost subdomain to localhost", in: "http://demo-app.localhost:4017", want: "http://localhost:4017"},
		{name: "http base", in: "http://example.com", want: "http://example.com"},
		{name: "https base", in: "https://example.com/", want: "https://example.com"},
		{name: "bare host", in: "localhost:4017", want: "http://localhost:4017"},
		{name: "invalid ws scheme", in: "ws://localhost:4017", wantErr: true},
		{name: "invalid wss scheme", in: "wss://example.com", wantErr: true},
		{name: "invalid relay path", in: "http://localhost:4017/relay", wantErr: true},
		{name: "invalid scheme", in: "ftp://example.com", wantErr: true},
		{name: "empty", in: "", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeRelayAPIURL(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q, got none", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeRelayAPIURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFirstRelayAPIURL(t *testing.T) {
	t.Parallel()

	got, err := firstRelayAPIURL([]string{"invalid://relay", "http://localhost:4017"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "http://localhost:4017" {
		t.Fatalf("unexpected relay URL: got %q", got)
	}

	if _, err := firstRelayAPIURL(nil); err == nil {
		t.Fatal("expected error with no bootstrap servers")
	}
}

func TestRelayConnectURL(t *testing.T) {
	t.Parallel()

	got, err := relayConnectURL("http://localhost:4017", "lease-1", "token-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, "ws://localhost:4017/api/connect?") {
		t.Fatalf("unexpected URL prefix: %q", got)
	}
	if !strings.Contains(got, "lease_id=lease-1") {
		t.Fatalf("missing lease_id in URL: %q", got)
	}
	if !strings.Contains(got, "token=token-1") {
		t.Fatalf("missing token in URL: %q", got)
	}

	if _, err := relayConnectURL("http://localhost:4017", "", "token-1"); err == nil {
		t.Fatal("expected error for empty lease ID")
	}
	if _, err := relayConnectURL("http://localhost:4017", "lease-1", ""); err == nil {
		t.Fatal("expected error for empty token")
	}
}
