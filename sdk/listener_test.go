package sdk

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/types"
)

func TestListenerSnapshotAndAddr(t *testing.T) {
	t.Parallel()

	listener := &Listener{
		leaseID:   "lease-1",
		hostnames: []string{"app.relay.example.com"},
		state:     listenerStateReady,
	}

	if listener.Addr().String() != "portal:lease-1" {
		t.Fatalf("Addr().String() = %q, want %q", listener.Addr().String(), "portal:lease-1")
	}

	publicURLs := listener.publicURLs()
	if len(publicURLs) != 1 || publicURLs[0] != "https://app.relay.example.com" {
		t.Fatalf("publicURLs() = %#v, want [https://app.relay.example.com]", publicURLs)
	}
}

func TestListenerAccept(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverConn1, clientConn1 := net.Pipe()
	defer clientConn1.Close()
	serverConn2, clientConn2 := net.Pipe()
	defer clientConn2.Close()

	listener := &Listener{
		ctx:      ctx,
		cancel:   cancel,
		accepted: make(chan net.Conn, 2),
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

func TestNewListenerRetriesBootstrapUntilSuccess(t *testing.T) {
	t.Parallel()

	var registerCalls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case types.PathSDKRegister:
			if registerCalls.Add(1) < 3 {
				writeTestAPIEnvelope(w, http.StatusServiceUnavailable, types.APIEnvelope[struct{}]{
					OK: false,
					Error: &types.APIError{
						Code:    types.APIErrorCodeLeaseRejected,
						Message: "relay unavailable",
					},
				})
				return
			}

			writeTestAPIEnvelope(w, http.StatusOK, types.APIEnvelope[types.RegisterResponse]{
				OK: true,
				Data: types.RegisterResponse{
					LeaseID: "lease-1",
					Metadata: types.LeaseMetadata{
						Owner: "alice",
					},
				},
			})
		case types.PathSDKRenew:
			writeTestAPIEnvelope(w, http.StatusOK, types.APIEnvelope[types.RenewResponse]{
				OK:   true,
				Data: types.RenewResponse{LeaseID: "lease-1"},
			})
		case types.PathSDKUnregister:
			writeTestAPIEnvelope(w, http.StatusOK, types.APIEnvelope[struct{}]{OK: true})
		case types.PathSDKConnect:
			writeTestAPIEnvelope(w, http.StatusServiceUnavailable, types.APIEnvelope[struct{}]{
				OK: false,
				Error: &types.APIError{
					Code:    types.APIErrorCodeSessionCreateFailed,
					Message: "reverse sessions unavailable",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	rootCAPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: server.Certificate().Raw,
	})

	client, err := NewRelayClient(server.URL, WithRootCAPEM(rootCAPEM))
	if err != nil {
		t.Fatalf("NewRelayClient() error = %v", err)
	}

	listener, err := NewListener(context.Background(), ListenRequest{
		Name: "demo",
	}, listenerOptions{
		client:             client,
		handshakeTimeout:   time.Second,
		renewBefore:        time.Second,
		retryCount:         defaultListenerRetryCount,
		retryDelay:         10 * time.Millisecond,
		defaultLeaseTTL:    time.Minute,
		defaultReadyTarget: 1,
	})
	if err != nil {
		t.Fatalf("newListener() error = %v", err)
	}
	defer listener.Close()

	waitForListenerCondition(t, func() bool {
		listener.mu.Lock()
		defer listener.mu.Unlock()
		return listener.leaseID == "lease-1"
	})

	if got := registerCalls.Load(); got < 3 {
		t.Fatalf("register call count = %d, want at least 3", got)
	}
}

func TestNewListenerBecomesStaleAfterRetryLimit(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == types.PathSDKRegister {
			writeTestAPIEnvelope(w, http.StatusServiceUnavailable, types.APIEnvelope[struct{}]{
				OK: false,
				Error: &types.APIError{
					Code:    types.APIErrorCodeLeaseRejected,
					Message: "relay unavailable",
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	rootCAPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: server.Certificate().Raw,
	})

	client, err := NewRelayClient(server.URL, WithRootCAPEM(rootCAPEM))
	if err != nil {
		t.Fatalf("NewRelayClient() error = %v", err)
	}

	listener, err := NewListener(context.Background(), ListenRequest{
		Name: "demo",
	}, listenerOptions{
		client:             client,
		handshakeTimeout:   time.Second,
		renewBefore:        time.Second,
		retryCount:         2,
		retryDelay:         10 * time.Millisecond,
		defaultLeaseTTL:    time.Minute,
		defaultReadyTarget: 1,
	})
	if err != nil {
		t.Fatalf("newListener() error = %v", err)
	}
	defer listener.Close()

	waitForListenerCondition(t, func() bool {
		listener.mu.Lock()
		defer listener.mu.Unlock()
		return listener.state == listenerStateStale
	})

	if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept() error = %v, want net.ErrClosed", err)
	}
}

func TestListenerReactivateAfterStale(t *testing.T) {
	t.Parallel()

	var allowRegister atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case types.PathSDKRegister:
			if allowRegister.Load() == 0 {
				writeTestAPIEnvelope(w, http.StatusServiceUnavailable, types.APIEnvelope[struct{}]{
					OK: false,
					Error: &types.APIError{
						Code:    types.APIErrorCodeLeaseRejected,
						Message: "relay unavailable",
					},
				})
				return
			}

			writeTestAPIEnvelope(w, http.StatusOK, types.APIEnvelope[types.RegisterResponse]{
				OK: true,
				Data: types.RegisterResponse{
					LeaseID: "lease-2",
					Metadata: types.LeaseMetadata{
						Owner: "alice",
					},
				},
			})
		case types.PathSDKRenew:
			writeTestAPIEnvelope(w, http.StatusOK, types.APIEnvelope[types.RenewResponse]{
				OK:   true,
				Data: types.RenewResponse{LeaseID: "lease-2"},
			})
		case types.PathSDKUnregister:
			writeTestAPIEnvelope(w, http.StatusOK, types.APIEnvelope[struct{}]{OK: true})
		case types.PathSDKConnect:
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijacking")
			}
			conn, rw, err := hijacker.Hijack()
			if err != nil {
				t.Fatalf("Hijack() error = %v", err)
			}
			_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
			_ = rw.Flush()
			time.Sleep(2 * time.Second)
			_ = conn.Close()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	rootCAPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: server.Certificate().Raw,
	})

	client, err := NewRelayClient(server.URL, WithRootCAPEM(rootCAPEM))
	if err != nil {
		t.Fatalf("NewRelayClient() error = %v", err)
	}

	listener, err := NewListener(context.Background(), ListenRequest{
		Name: "demo",
	}, listenerOptions{
		client:             client,
		handshakeTimeout:   time.Second,
		renewBefore:        time.Second,
		retryCount:         1,
		retryDelay:         10 * time.Millisecond,
		defaultLeaseTTL:    time.Minute,
		defaultReadyTarget: 1,
	})
	if err != nil {
		t.Fatalf("newListener() error = %v", err)
	}
	defer listener.Close()

	waitForListenerCondition(t, func() bool {
		listener.mu.Lock()
		defer listener.mu.Unlock()
		return listener.state == listenerStateStale
	})

	allowRegister.Store(1)
	if err := listener.Reactivate(context.Background()); err != nil {
		t.Fatalf("Reactivate() error = %v", err)
	}

	waitForListenerCondition(t, func() bool {
		listener.mu.Lock()
		defer listener.mu.Unlock()
		return listener.state == listenerStateReady && listener.leaseID == "lease-2"
	})
}

func TestListenerAcceptDoesNotReturnQueuedConnAfterClose(t *testing.T) {
	t.Parallel()

	conn := &listenerStubConn{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	listener := &Listener{
		ctx:      ctx,
		cancel:   cancel,
		accepted: make(chan net.Conn, 1),
	}
	listener.accepted <- conn

	gotConn, err := listener.Accept()
	if gotConn != nil {
		t.Fatalf("Accept() conn = %#v, want nil", gotConn)
	}
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept() error = %v, want net.ErrClosed", err)
	}
	if conn.closeCount != 1 {
		t.Fatalf("conn close count = %d, want 1", conn.closeCount)
	}
}

type listenerStubConn struct {
	closeCount int
}

func (c *listenerStubConn) Read(_ []byte) (int, error)         { return 0, nil }
func (c *listenerStubConn) Write(b []byte) (int, error)        { return len(b), nil }
func (c *listenerStubConn) Close() error                       { c.closeCount++; return nil }
func (c *listenerStubConn) LocalAddr() net.Addr                { return listenerAddr("stub-local") }
func (c *listenerStubConn) RemoteAddr() net.Addr               { return listenerAddr("stub-remote") }
func (c *listenerStubConn) SetDeadline(_ time.Time) error      { return nil }
func (c *listenerStubConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *listenerStubConn) SetWriteDeadline(_ time.Time) error { return nil }

func waitForListenerCondition(t *testing.T, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("timed out waiting for listener condition")
}

func writeTestAPIEnvelope[T any](w http.ResponseWriter, status int, envelope types.APIEnvelope[T]) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope)
}
