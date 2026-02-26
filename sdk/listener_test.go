package sdk

import (
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"

	"gosuda.org/portal/portal"
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
	if !strings.HasPrefix(got, "ws://localhost:4017/sdk/connect?") {
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

func TestWaitForReverseStart_HTTPMode(t *testing.T) {
	t.Parallel()

	l := &Listener{stopCh: make(chan struct{})}
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	done := make(chan error, 1)
	go func() {
		done <- l.waitForReverseStart(local, portal.HTTPStartMarker)
	}()

	_, err := peer.Write([]byte{portal.HTTPStartMarker})
	if err != nil {
		t.Fatalf("write marker: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForReverseStart failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for marker")
	}
}

func TestWaitForReverseStart_TLSMode(t *testing.T) {
	t.Parallel()

	l := &Listener{
		stopCh:    make(chan struct{}),
		tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	done := make(chan error, 1)
	go func() {
		done <- l.waitForReverseStart(local, portal.TLSStartMarker)
	}()

	_, err := peer.Write([]byte{portal.TLSStartMarker})
	if err != nil {
		t.Fatalf("write marker: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForReverseStart failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for marker")
	}
}

func TestWaitForReverseStart_IgnoresKeepaliveMarker(t *testing.T) {
	t.Parallel()

	l := &Listener{stopCh: make(chan struct{})}
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	done := make(chan error, 1)
	go func() {
		done <- l.waitForReverseStart(local, portal.HTTPStartMarker)
	}()

	_, err := peer.Write([]byte{portal.ReverseKeepaliveMarker})
	if err != nil {
		t.Fatalf("write keepalive marker: %v", err)
	}
	_, err = peer.Write([]byte{portal.HTTPStartMarker})
	if err != nil {
		t.Fatalf("write start marker: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForReverseStart failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for marker")
	}
}

func TestWaitForReverseStart_TLSRejectsHTTPMarker(t *testing.T) {
	t.Parallel()

	l := &Listener{
		stopCh:    make(chan struct{}),
		tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	done := make(chan error, 1)
	go func() {
		done <- l.waitForReverseStart(local, portal.TLSStartMarker)
	}()

	_, err := peer.Write([]byte{portal.HTTPStartMarker})
	if err != nil {
		t.Fatalf("write marker: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected invalid marker error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for marker")
	}
}

func TestWaitForReverseStart_HTTPRejectsTLSMarker(t *testing.T) {
	t.Parallel()

	l := &Listener{stopCh: make(chan struct{})}
	local, peer := net.Pipe()
	defer local.Close()
	defer peer.Close()

	done := make(chan error, 1)
	go func() {
		done <- l.waitForReverseStart(local, portal.HTTPStartMarker)
	}()

	_, err := peer.Write([]byte{portal.TLSStartMarker})
	if err != nil {
		t.Fatalf("write marker: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected invalid marker error")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for marker")
	}
}
