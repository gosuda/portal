package portal

import (
	"io"
	"net"
	"strings"
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

func TestReverseHubOfferRejectsInvalidInput(t *testing.T) {
	hub := NewReverseHub()

	if ok := hub.Offer("", nil); ok {
		t.Fatal("expected offer with empty lease and nil connection to fail")
	}
	if ok := hub.Offer("   ", nil); ok {
		t.Fatal("expected offer with whitespace lease and nil connection to fail")
	}
}

func TestHandleConnectTrimsLeaseIDAndToken(t *testing.T) {
	hub := NewReverseHub()
	leaseID := "lease-connect-trim"
	token := "token-connect-trim"
	hub.SetAuthorizer(func(gotLeaseID, gotToken string) bool {
		return gotLeaseID == leaseID && gotToken == token
	})

	local, peer := net.Pipe()
	defer func() {
		_ = peer.Close()
	}()

	done := make(chan struct{})
	go func() {
		hub.HandleConnect(local, " "+leaseID+" ", " "+token+" ", " 127.0.0.1 ")
		close(done)
	}()

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
	deadline := time.Now().Add(500 * time.Millisecond)
	for err != nil {
		if time.Now().After(deadline) {
			t.Fatalf("AcquireForTLS failed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
		got, err = hub.AcquireForTLS(leaseID, 25*time.Millisecond)
	}
	if got == nil {
		t.Fatal("AcquireForTLS returned nil connection")
	}

	select {
	case err := <-readErr:
		t.Fatalf("failed to read marker: %v", err)
	case marker := <-markerRead:
		if marker != TLSStartMarker {
			t.Fatalf("unexpected marker: %d", marker)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for start marker")
	}

	got.Close()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("HandleConnect did not return after connection close")
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

func TestAcquireForTLSPollLoopSendsStartMarker(t *testing.T) {
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

	var (
		got *ReverseConn
		err error
	)
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		got, err = hub.AcquireForTLS(leaseID, 25*time.Millisecond)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("AcquireForTLS failed: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
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

func TestHandleConnectOffersAuthorizedConn(t *testing.T) {
	hub := NewReverseHub()
	leaseID := "lease-connect"
	token := "reverse-token"
	accepted := make(chan struct{}, 1)
	hub.SetAuthorizer(func(gotLeaseID, gotToken string) bool {
		return gotLeaseID == leaseID && gotToken == token
	})
	hub.SetOnAccepted(func(gotLeaseID, _ string) {
		if gotLeaseID != leaseID {
			return
		}
		select {
		case accepted <- struct{}{}:
		default:
		}
	})

	local, peer := net.Pipe()
	defer func() {
		_ = peer.Close()
	}()

	done := make(chan struct{})
	go func() {
		hub.HandleConnect(local, leaseID, token, "127.0.0.1")
		close(done)
	}()

	select {
	case <-accepted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("HandleConnect did not reach accepted state")
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
	if got == nil {
		t.Fatal("AcquireForTLS returned nil connection")
	}

	select {
	case err := <-readErr:
		t.Fatalf("failed to read start marker: %v", err)
	case b := <-markerRead:
		if b != TLSStartMarker {
			t.Fatalf("unexpected marker: %d", b)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for start marker")
	}

	got.Close()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("HandleConnect did not return after connection close")
	}
}

func TestHandleConnectUnauthorizedHonorsAuthDelay(t *testing.T) {
	hub := NewReverseHub()
	leaseID := "lease-auth-delay"
	hub.SetAuthorizer(func(gotLeaseID, gotToken string) bool {
		return gotLeaseID == leaseID && gotToken == "expected-token"
	})

	local, peer := net.Pipe()
	defer func() {
		_ = peer.Close()
	}()

	done := make(chan struct{})
	go func() {
		hub.HandleConnect(local, leaseID, "wrong-token", "127.0.0.1")
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("HandleConnect returned before auth delay elapsed")
	case <-time.After(AuthFailureDelay / 2):
	}

	select {
	case <-done:
	case <-time.After(AuthFailureDelay + 500*time.Millisecond):
		t.Fatal("HandleConnect did not return after auth delay")
	}

	_ = peer.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	var b [1]byte
	if _, err := peer.Read(b[:]); err == nil {
		t.Fatal("expected connection to be closed after unauthorized connect")
	}
}

func TestHandleConnectBannedIPHonorsAuthDelay(t *testing.T) {
	hub := NewReverseHub()
	leaseID := "lease-banned-ip"
	token := "token-ok"
	blockedIP := "203.0.113.50"

	hub.SetAuthorizer(func(gotLeaseID, gotToken string) bool {
		return gotLeaseID == leaseID && gotToken == token
	})
	hub.SetIPBanChecker(func(ip string) bool {
		return ip == blockedIP
	})

	local, peer := net.Pipe()
	defer func() {
		_ = peer.Close()
	}()

	done := make(chan struct{})
	go func() {
		hub.HandleConnect(local, leaseID, token, blockedIP)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("HandleConnect returned before auth delay elapsed for banned IP")
	case <-time.After(AuthFailureDelay / 2):
	}

	select {
	case <-done:
	case <-time.After(AuthFailureDelay + 500*time.Millisecond):
		t.Fatal("HandleConnect did not return after banned-IP auth delay")
	}

	_, err := hub.AcquireForTLS(leaseID, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected no reverse tunnel for banned IP")
	}
	if !strings.Contains(err.Error(), "no tunnel available") {
		t.Fatalf("unexpected acquire error after banned IP connect: %v", err)
	}

	_ = peer.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	var b [1]byte
	if _, err := peer.Read(b[:]); err == nil {
		t.Fatal("expected banned reverse connection to be closed")
	}
}

func TestDropLeaseCleansPoolImmediately(t *testing.T) {
	hub := NewReverseHub()
	leaseID := "lease-drop"

	local, peer := net.Pipe()
	defer func() {
		_ = peer.Close()
	}()
	conn := NewReverseConn(local)
	defer conn.Close()

	if ok := hub.Offer(leaseID, conn); !ok {
		t.Fatal("offer failed")
	}

	start := time.Now()
	hub.DropLease(leaseID)

	_, err := hub.AcquireForTLS(leaseID, 2*time.Second)
	if err == nil {
		t.Fatal("expected acquire to fail after lease drop")
	}
	if !strings.Contains(err.Error(), "no tunnel available") {
		t.Fatalf("unexpected error after lease drop: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("expected immediate cleanup after drop, acquire took %v", elapsed)
	}

	otherLocal, otherPeer := net.Pipe()
	defer func() {
		_ = otherPeer.Close()
	}()
	otherConn := NewReverseConn(otherLocal)
	defer otherConn.Close()
	if ok := hub.Offer(leaseID, otherConn); ok {
		t.Fatal("expected dropped lease to reject new offered connections")
	}

	_ = peer.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	var b [1]byte
	if _, err := peer.Read(b[:]); err == nil {
		t.Fatal("expected dropped pooled connection to be closed")
	}
}
