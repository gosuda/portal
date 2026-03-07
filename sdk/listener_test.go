package sdk

import (
	"net"
	"testing"
)

func TestListenerSingleEntryAccessors(t *testing.T) {
	t.Parallel()

	listener := &Listener{
		entries: []*listenerLease{
			{
				info: ListenerEntry{
					RelayURL:  "https://relay.example.com",
					LeaseID:   "lease-1",
					Hostnames: []string{"app.relay.example.com"},
				},
			},
		},
	}

	entry, ok := listener.singleEntry()
	if !ok {
		t.Fatal("singleEntry() ok = false, want true")
	}
	if entry.LeaseID != "lease-1" {
		t.Fatalf("singleEntry().LeaseID = %q, want %q", entry.LeaseID, "lease-1")
	}

	publicURLs := listener.PublicURLs()
	if len(publicURLs) != 1 || publicURLs[0] != "https://app.relay.example.com" {
		t.Fatalf("PublicURLs() = %#v, want [https://app.relay.example.com]", publicURLs)
	}
}

func TestListenerMultiEntryAccessors(t *testing.T) {
	t.Parallel()

	listener := &Listener{
		entries: []*listenerLease{
			{
				info: ListenerEntry{
					RelayURL:  "https://relay-a.example.com",
					LeaseID:   "lease-a",
					Hostnames: []string{"a.example.com"},
				},
			},
			{
				info: ListenerEntry{
					RelayURL:  "https://relay-b.example.com",
					LeaseID:   "lease-b",
					Hostnames: []string{"b.example.com"},
				},
			},
		},
	}

	if _, ok := listener.singleEntry(); ok {
		t.Fatal("singleEntry() ok = true, want false")
	}

	entries := listener.Entries()
	if len(entries) != 2 {
		t.Fatalf("Entries() len = %d, want 2", len(entries))
	}

	publicURLs := listener.PublicURLs()
	if len(publicURLs) != 2 {
		t.Fatalf("PublicURLs() len = %d, want 2", len(publicURLs))
	}
}

func TestListenerAcceptEntry(t *testing.T) {
	t.Parallel()

	done := make(chan struct{})
	serverConn1, clientConn1 := net.Pipe()
	defer clientConn1.Close()
	serverConn2, clientConn2 := net.Pipe()
	defer clientConn2.Close()

	listener := &Listener{
		ctxDone:  done,
		accepted: make(chan acceptedConn, 1),
	}
	listener.accepted <- acceptedConn{
		conn: serverConn1,
		entry: ListenerEntry{
			RelayURL:  "https://relay.example.com",
			LeaseID:   "lease-1",
			Hostnames: []string{"app.relay.example.com"},
		},
	}

	conn, entry, err := listener.AcceptEntry()
	if err != nil {
		t.Fatalf("AcceptEntry() error = %v", err)
	}
	defer conn.Close()

	if conn != serverConn1 {
		t.Fatal("AcceptEntry() did not return the original connection")
	}
	if entry.LeaseID != "lease-1" {
		t.Fatalf("AcceptEntry().LeaseID = %q, want %q", entry.LeaseID, "lease-1")
	}

	listener.accepted <- acceptedConn{conn: serverConn2}
	plainConn, err := listener.Accept()
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	defer plainConn.Close()
	if plainConn != serverConn2 {
		t.Fatal("Accept() did not return the original connection")
	}
}
