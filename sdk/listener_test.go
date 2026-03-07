package sdk

import (
	"net"
	"testing"

	"github.com/gosuda/portal/v2/types"
)

func TestListenerAccessors(t *testing.T) {
	t.Parallel()

	listener := &Listener{
		leaseID:   "lease-1",
		hostnames: []string{"app.relay.example.com"},
		metadata: types.LeaseMetadata{
			Owner: "alice",
			Tags:  []string{"one", "two"},
		},
	}

	if listener.Addr().String() != "portal:lease-1" {
		t.Fatalf("Addr().String() = %q, want %q", listener.Addr().String(), "portal:lease-1")
	}
	if listener.LeaseID() != "lease-1" {
		t.Fatalf("LeaseID() = %q, want %q", listener.LeaseID(), "lease-1")
	}

	hostnames := listener.Hostnames()
	if len(hostnames) != 1 || hostnames[0] != "app.relay.example.com" {
		t.Fatalf("Hostnames() = %#v, want [app.relay.example.com]", hostnames)
	}

	metadata := listener.Metadata()
	if metadata.Owner != "alice" {
		t.Fatalf("Metadata().Owner = %q, want %q", metadata.Owner, "alice")
	}
	if len(metadata.Tags) != 2 {
		t.Fatalf("Metadata().Tags len = %d, want 2", len(metadata.Tags))
	}

	publicURLs := listener.PublicURLs()
	if len(publicURLs) != 1 || publicURLs[0] != "https://app.relay.example.com" {
		t.Fatalf("PublicURLs() = %#v, want [https://app.relay.example.com]", publicURLs)
	}
}

func TestListenerAccept(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	serverConn1, clientConn1 := net.Pipe()
	defer clientConn1.Close()
	serverConn2, clientConn2 := net.Pipe()
	defer clientConn2.Close()

	listener := &Listener{
		ctxDone:   done,
		accepted:  make(chan net.Conn, 2),
		hostnames: []string{"app.relay.example.com"},
	}
	listener.accepted <- serverConn1
	listener.accepted <- serverConn2

	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	defer conn.Close()

	if conn != serverConn1 {
		t.Fatal("Accept() did not return the original connection")
	}

	plainConn, err := listener.Accept()
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	defer plainConn.Close()
	if plainConn != serverConn2 {
		t.Fatal("Accept() did not return the original connection")
	}
}
