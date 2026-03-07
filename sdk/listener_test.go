package sdk

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/gosuda/portal/v2/types"
)

func TestListenerSingleEntryAccessors(t *testing.T) {
	t.Parallel()

	listener := newListener(context.Background())
	listener.entries = []*listenerLease{
		{
			info: ListenerEntry{
				RelayURL:  "https://relay.example.com",
				LeaseID:   "lease-1",
				Hostnames: []string{"app.relay.example.com"},
			},
			active: true,
		},
	}

	entries := listener.Entries()
	if len(entries) != 1 {
		t.Fatalf("Entries() len = %d, want 1", len(entries))
	}
	if listener.Addr().String() != "portal:lease-1" {
		t.Fatalf("Addr().String() = %q, want %q", listener.Addr().String(), "portal:lease-1")
	}

	publicURLs := listener.PublicURLs()
	if len(publicURLs) != 1 || publicURLs[0] != "https://app.relay.example.com" {
		t.Fatalf("PublicURLs() = %#v, want [https://app.relay.example.com]", publicURLs)
	}
}

func TestListenerMultiEntryAccessors(t *testing.T) {
	t.Parallel()

	listener := newListener(context.Background())
	listener.entries = []*listenerLease{
		{
			info: ListenerEntry{
				RelayURL:  "https://relay-a.example.com",
				LeaseID:   "lease-a",
				Hostnames: []string{"a.example.com"},
			},
			active: true,
		},
		{
			info: ListenerEntry{
				RelayURL:  "https://relay-b.example.com",
				LeaseID:   "lease-b",
				Hostnames: []string{"b.example.com"},
			},
			active: true,
		},
	}

	if listener.Addr().String() != "portal:multi" {
		t.Fatalf("Addr().String() = %q, want %q", listener.Addr().String(), "portal:multi")
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

func TestListenerEntriesSkipInactiveLeases(t *testing.T) {
	t.Parallel()

	listener := newListener(context.Background())
	listener.entries = []*listenerLease{
		{
			info: ListenerEntry{
				RelayURL:  "https://relay-a.example.com",
				LeaseID:   "lease-a",
				Hostnames: []string{"a.example.com"},
			},
			active: true,
		},
		{
			info: ListenerEntry{
				RelayURL:  "https://relay-b.example.com",
				LeaseID:   "lease-b",
				Hostnames: []string{"b.example.com"},
			},
			active: false,
		},
	}

	entries := listener.Entries()
	if len(entries) != 1 {
		t.Fatalf("Entries() len = %d, want 1", len(entries))
	}
	if entries[0].LeaseID != "lease-a" {
		t.Fatalf("Entries()[0].LeaseID = %q, want %q", entries[0].LeaseID, "lease-a")
	}
}

func TestListenerAcceptEntry(t *testing.T) {
	t.Parallel()

	listener := newListener(context.Background())
	listener.accepted = make(chan acceptedConn, 2)

	serverConn1, clientConn1 := net.Pipe()
	defer clientConn1.Close()
	serverConn2, clientConn2 := net.Pipe()
	defer clientConn2.Close()

	listener.accepted <- acceptedConn{
		conn: serverConn1,
		entry: ListenerEntry{
			RelayURL:  "https://relay.example.com",
			LeaseID:   "lease-1",
			Hostnames: []string{"app.relay.example.com"},
		},
	}
	listener.accepted <- acceptedConn{conn: serverConn2}

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

	plainConn, err := listener.Accept()
	if err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	defer plainConn.Close()
	if plainConn != serverConn2 {
		t.Fatal("Accept() did not return the original connection")
	}
}

func TestListenerAcceptEntryReturnsTerminalError(t *testing.T) {
	t.Parallel()

	listener := newListener(context.Background())
	listener.accepted = make(chan acceptedConn, 1)

	wantErr := &types.APIRequestError{Code: types.APIErrorCodeUnauthorized, Message: "bad reverse token"}
	listener.fail(wantErr)

	conn, entry, err := listener.AcceptEntry()
	if conn != nil {
		t.Fatal("AcceptEntry() conn != nil, want nil")
	}
	if entry.RelayURL != "" || entry.LeaseID != "" || len(entry.Hostnames) != 0 || len(entry.Metadata.Tags) != 0 {
		t.Fatalf("AcceptEntry() entry = %#v, want zero value", entry)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("AcceptEntry() error = %v, want %v", err, wantErr)
	}
}

func TestListenerStopEntryKeepsOtherLeasesActive(t *testing.T) {
	t.Parallel()

	listener := newListener(context.Background())
	entryA := &listenerLease{
		parent: listener,
		info: ListenerEntry{
			RelayURL:  "https://relay-a.example.com",
			LeaseID:   "lease-a",
			Hostnames: []string{"a.example.com"},
		},
		active: true,
	}
	entryB := &listenerLease{
		parent: listener,
		info: ListenerEntry{
			RelayURL:  "https://relay-b.example.com",
			LeaseID:   "lease-b",
			Hostnames: []string{"b.example.com"},
		},
		active: true,
	}
	listener.entries = []*listenerLease{entryA, entryB}
	listener.activeCount = 2

	entryA.stop(&types.APIRequestError{Code: types.APIErrorCodeUnauthorized, Message: "stopped"})

	if listener.isClosed() {
		t.Fatal("listener closed after stopping one entry, want active")
	}

	entries := listener.Entries()
	if len(entries) != 1 {
		t.Fatalf("Entries() len = %d, want 1", len(entries))
	}
	if entries[0].LeaseID != "lease-b" {
		t.Fatalf("Entries()[0].LeaseID = %q, want %q", entries[0].LeaseID, "lease-b")
	}
}

func TestListenerStopLastEntryCancelsListener(t *testing.T) {
	t.Parallel()

	listener := newListener(context.Background())
	entry := &listenerLease{
		parent: listener,
		info: ListenerEntry{
			RelayURL:  "https://relay.example.com",
			LeaseID:   "lease-1",
			Hostnames: []string{"app.relay.example.com"},
		},
		active: true,
	}
	listener.entries = []*listenerLease{entry}
	listener.activeCount = 1
	listener.accepted = make(chan acceptedConn, 1)

	wantErr := &types.APIRequestError{Code: types.APIErrorCodeLeaseNotFound, Message: "lease disappeared"}
	entry.stop(wantErr)

	if !listener.isClosed() {
		t.Fatal("listener is still active after stopping last entry")
	}

	_, _, err := listener.AcceptEntry()
	if !errors.Is(err, wantErr) {
		t.Fatalf("AcceptEntry() error = %v, want %v", err, wantErr)
	}
}

func TestListenerEntryCloneDeepCopiesMetadataTags(t *testing.T) {
	t.Parallel()

	entry := ListenerEntry{
		RelayURL:  "https://relay.example.com",
		LeaseID:   "lease-1",
		Hostnames: []string{"app.relay.example.com"},
		Metadata: types.LeaseMetadata{
			Owner: "alice",
			Tags:  []string{"one", "two"},
		},
	}

	clone := entry.clone()
	clone.Hostnames[0] = "changed.example.com"
	clone.Metadata.Tags[0] = "changed"

	if entry.Hostnames[0] != "app.relay.example.com" {
		t.Fatalf("entry.Hostnames[0] = %q, want %q", entry.Hostnames[0], "app.relay.example.com")
	}
	if entry.Metadata.Tags[0] != "one" {
		t.Fatalf("entry.Metadata.Tags[0] = %q, want %q", entry.Metadata.Tags[0], "one")
	}
}
