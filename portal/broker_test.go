package portal

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/types"
)

const brokerAsyncTestTimeout = 5 * time.Second

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

	claimCtx, cancel := context.WithTimeout(context.Background(), brokerAsyncTestTimeout)
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
	case <-time.After(brokerAsyncTestTimeout):
		t.Fatal("timed out waiting for activation marker")
	}
}

func TestLeaseBrokerCloseClosesIdleSessions(t *testing.T) {
	t.Parallel()

	serverConn, clientConn := net.Pipe()
	broker := newLeaseBroker("lease-test", time.Hour, 2)
	session := newReverseSession(serverConn, time.Hour)
	if err := broker.Offer(session); err != nil {
		t.Fatalf("Offer() error = %v", err)
	}

	broker.Close()

	buf := make([]byte, 1)
	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := clientConn.Read(buf); err == nil {
		t.Fatal("Read() succeeded, want connection close")
	}
}

func TestLeaseBrokerCloseUnblocksClaim(t *testing.T) {
	t.Parallel()

	broker := newLeaseBroker("lease-test", time.Hour, 2)
	claimCtx, cancel := context.WithTimeout(context.Background(), brokerAsyncTestTimeout)
	defer cancel()

	started := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		close(started)
		_, err := broker.Claim(claimCtx)
		errCh <- err
	}()

	<-started
	select {
	case err := <-errCh:
		t.Fatalf("Claim() returned before Close(): %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	broker.Close()

	select {
	case err := <-errCh:
		if !errors.Is(err, errBrokerClosed) {
			t.Fatalf("Claim() error = %v, want %v", err, errBrokerClosed)
		}
	case <-time.After(brokerAsyncTestTimeout):
		t.Fatal("timed out waiting for closed claim")
	}
}

func TestLeaseBrokerClaimWaitsForLateOffer(t *testing.T) {
	t.Parallel()

	broker := newLeaseBroker("lease-test", time.Hour, 2)

	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	session := newReverseSession(serverConn, time.Hour)
	claimCtx, cancel := context.WithTimeout(context.Background(), brokerAsyncTestTimeout)
	defer cancel()

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

	type claimResult struct {
		session *reverseSession
		err     error
	}
	started := make(chan struct{})
	resultCh := make(chan claimResult, 1)
	go func() {
		close(started)
		claimed, err := broker.Claim(claimCtx)
		resultCh <- claimResult{session: claimed, err: err}
	}()

	<-started
	select {
	case result := <-resultCh:
		t.Fatalf("Claim() returned before Offer(): %#v", result)
	case <-time.After(50 * time.Millisecond):
	}

	if err := broker.Offer(session); err != nil {
		t.Fatalf("Offer() error = %v", err)
	}

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("Claim() error = %v", result.err)
		}
		if result.session != session {
			t.Fatalf("Claim() returned unexpected session")
		}
		select {
		case err := <-errCh:
			t.Fatalf("ReadFull() error = %v", err)
		case marker := <-markerCh:
			if marker != types.MarkerTLSStart {
				t.Fatalf("marker = 0x%02x, want 0x%02x", marker, types.MarkerTLSStart)
			}
		case <-time.After(brokerAsyncTestTimeout):
			t.Fatal("timed out waiting for activation marker")
		}
	case <-time.After(brokerAsyncTestTimeout):
		t.Fatal("timed out waiting for claim")
	}
}

func TestLeaseBrokerClaimTimesOutWithoutSessions(t *testing.T) {
	t.Parallel()

	broker := newLeaseBroker("lease-test", time.Hour, 2)

	claimCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := broker.Claim(claimCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Claim() error = %v, want %v", err, context.DeadlineExceeded)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatalf("Claim() returned too early: %v", time.Since(start))
	}
}
