package portal

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/types"
)

func TestLeaseBrokerClaimActivatesTLSMarker(t *testing.T) {
	t.Parallel()

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	broker := newLeaseBroker("lease-test", time.Hour, 2)
	session := newReverseSession(serverConn, time.Hour)
	if err := broker.Offer(session); err != nil {
		t.Fatalf("Offer() error = %v", err)
	}

	markerCh := make(chan byte, 1)
	errCh := make(chan error, 1)
	go func() {
		var marker [1]byte
		if _, err := io.ReadFull(clientConn, marker[:]); err != nil {
			errCh <- err
			return
		}
		markerCh <- marker[0]
	}()

	claimCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	claimed, err := broker.Claim(claimCtx)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed != session {
		t.Fatalf("Claim() returned unexpected session")
	}

	select {
	case err := <-errCh:
		t.Fatalf("ReadFull() error = %v", err)
	case marker := <-markerCh:
		if marker != types.MarkerTLSStart {
			t.Fatalf("marker = 0x%02x, want 0x%02x", marker, types.MarkerTLSStart)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for activation marker")
	}
}

func TestLeaseBrokerDropClosesIdleSessions(t *testing.T) {
	t.Parallel()

	serverConn, clientConn := net.Pipe()
	broker := newLeaseBroker("lease-test", time.Hour, 2)
	session := newReverseSession(serverConn, time.Hour)
	if err := broker.Offer(session); err != nil {
		t.Fatalf("Offer() error = %v", err)
	}

	broker.Drop()

	buf := make([]byte, 1)
	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := clientConn.Read(buf); err == nil {
		t.Fatal("Read() succeeded, want connection close")
	}
}
