package portal

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestReverseHubAuthorization(t *testing.T) {
	hub := NewReverseHub()

	if hub.isAuthorized("lease-1", "token-1") {
		t.Fatal("expected unauthorized when authorizer is not configured")
	}

	hub.SetAuthorizer(func(leaseID, token string) bool {
		return leaseID == "lease-1" && token == "token-1"
	})

	if !hub.isAuthorized("lease-1", "token-1") {
		t.Fatal("expected authorized")
	}
	if hub.isAuthorized("lease-1", "wrong-token") {
		t.Fatal("expected unauthorized for wrong token")
	}
}

func TestAcquireForTLSSendsStartMarker(t *testing.T) {
	hub := NewReverseHub()
	leaseID := "lease-tls-marker"

	local, peer := net.Pipe()
	defer func() {
		_ = peer.Close()
	}()
	conn := NewReverseConn(local)
	defer conn.Close()

	if ok := hub.Offer(leaseID, conn); !ok {
		t.Fatal("offer failed")
	}

	markerRead := make(chan byte, 1)
	readErr := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := io.ReadFull(peer, b[:])
		if err != nil {
			readErr <- err
			return
		}
		markerRead <- b[0]
	}()

	got, err := hub.AcquireForTLS(leaseID, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("AcquireForTLS failed: %v", err)
	}
	if got != conn {
		t.Fatal("AcquireForTLS returned unexpected connection")
	}

	select {
	case err := <-readErr:
		t.Fatalf("failed to read marker: %v", err)
	case b := <-markerRead:
		if b != TLSStartMarker {
			t.Fatalf("unexpected marker: %d", b)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for start marker")
	}
}

func TestAcquireForHTTPSendsStartMarker(t *testing.T) {
	hub := NewReverseHub()
	leaseID := "lease-http-marker"

	local, peer := net.Pipe()
	defer func() {
		_ = peer.Close()
	}()
	conn := NewReverseConn(local)
	defer conn.Close()

	if ok := hub.Offer(leaseID, conn); !ok {
		t.Fatal("offer failed")
	}

	markerRead := make(chan byte, 1)
	readErr := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := io.ReadFull(peer, b[:])
		if err != nil {
			readErr <- err
			return
		}
		markerRead <- b[0]
	}()

	got, err := hub.AcquireForHTTP(leaseID, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("AcquireForHTTP failed: %v", err)
	}
	if got != conn {
		t.Fatal("AcquireForHTTP returned unexpected connection")
	}

	select {
	case err := <-readErr:
		t.Fatalf("failed to read marker: %v", err)
	case b := <-markerRead:
		if b != HTTPStartMarker {
			t.Fatalf("unexpected marker: %d", b)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for start marker")
	}
}
