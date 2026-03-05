package portal

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"gosuda.org/portal/types"
)

func TestRelayServerReverseHubAuthorizerTrimsToken(t *testing.T) {
	serv, err := NewRelayServer(context.Background(), nil, ":0", "example.com", "", "")
	if err != nil {
		t.Fatalf("create relay server: %v", err)
	}

	lease := &types.Lease{
		ID:           "lease-authorizer-trim",
		Name:         "tenant",
		TLS:          true,
		ReverseToken: " reverse-token ",
		Expires:      time.Now().Add(time.Minute),
	}
	if ok := serv.GetLeaseManager().UpdateLease(lease); !ok {
		t.Fatal("failed to register lease in lease manager")
	}

	hub := serv.GetReverseHub()
	if !hub.isAuthorized(lease.ID, "reverse-token") {
		t.Fatal("expected authorizer to accept trimmed reverse token")
	}
	if !hub.isAuthorized(lease.ID, " reverse-token ") {
		t.Fatal("expected authorizer to accept token with surrounding whitespace")
	}
	if hub.isAuthorized(lease.ID, "wrong-token") {
		t.Fatal("expected authorizer to reject wrong token")
	}
}

func TestRelayServerHandleLeaseDeletedDropsRouteAndPool(t *testing.T) {
	serv, err := NewRelayServer(context.Background(), nil, ":0", "example.com", "", "")
	if err != nil {
		t.Fatalf("create relay server: %v", err)
	}

	const (
		leaseID  = "lease-delete-hook"
		leaseSNI = "tenant.example.com"
	)

	if err := serv.GetSNIRouter().RegisterRoute(leaseSNI, leaseID, "tenant"); err != nil {
		t.Fatalf("register route: %v", err)
	}

	local, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	conn := NewReverseConn(local)
	defer conn.Close()

	if ok := serv.GetReverseHub().Offer(leaseID, conn); !ok {
		t.Fatal("offer failed")
	}

	serv.handleLeaseDeleted(" " + leaseID + " ")

	if _, ok := serv.GetSNIRouter().GetRoute(leaseSNI); ok {
		t.Fatal("expected route to be removed when lease is deleted")
	}

	_, acquireErr := serv.GetReverseHub().AcquireForTLS(leaseID, 100*time.Millisecond)
	if acquireErr == nil {
		t.Fatal("expected reverse pool to be dropped when lease is deleted")
	}
	if !strings.Contains(acquireErr.Error(), "no tunnel available") {
		t.Fatalf("unexpected acquire error: %v", acquireErr)
	}
}

func TestRelayServerHandleRootFallbackRequiresRootHostMatch(t *testing.T) {
	serv, err := NewRelayServer(context.Background(), nil, ":0", "example.com", "", "")
	if err != nil {
		t.Fatalf("create relay server: %v", err)
	}

	client, peer := net.Pipe()
	defer func() {
		_ = client.Close()
		_ = peer.Close()
	}()

	handled := serv.handleRootFallback(client, "tenant.example.com", "portal.example.com", "127.0.0.1:4017")
	if handled {
		t.Fatal("expected non-root SNI to bypass fallback handler")
	}
}

func TestRelayServerHandleRootFallbackClosesClientOnDialFailure(t *testing.T) {
	serv, err := NewRelayServer(context.Background(), nil, ":0", "example.com", "", "")
	if err != nil {
		t.Fatalf("create relay server: %v", err)
	}

	client, peer := net.Pipe()
	defer func() { _ = peer.Close() }()

	handled := serv.handleRootFallback(client, "portal.example.com", "portal.example.com", "invalid-upstream")
	if !handled {
		t.Fatal("expected root SNI to be handled by fallback path")
	}

	_ = peer.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	var b [1]byte
	if _, err := peer.Read(b[:]); err == nil {
		t.Fatal("expected client connection to be closed after fallback dial failure")
	}
}

func TestRelayServerStopIsIdempotent(t *testing.T) {
	serv, err := NewRelayServer(context.Background(), nil, ":0", "example.com", "", "")
	if err != nil {
		t.Fatalf("create relay server: %v", err)
	}

	serv.Stop()
	serv.Stop()
}
