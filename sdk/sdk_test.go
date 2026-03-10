package sdk

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/types"
)

func TestNewRelayClientAcceptsMatchingVersion(t *testing.T) {
	t.Parallel()

	server := newDomainServer(t, types.SDKProtocolVersion)
	defer server.Close()

	api, err := newRelayClient(server.URL, ListenerConfig{Name: "demo"})
	if err != nil {
		t.Fatalf("newRelayClient() error = %v", err)
	}
	defer api.close()
}

func TestNewListenerRejectsVersionMismatch(t *testing.T) {
	t.Parallel()

	server := newDomainServer(t, "999")
	defer server.Close()

	listener, err := NewListener(context.Background(), server.URL, ListenerConfig{Name: "demo"})
	if err == nil {
		t.Fatal("NewListener() error = nil, want version mismatch")
	}
	if listener != nil {
		t.Fatalf("NewListener() listener = %#v, want nil", listener)
	}
	if !strings.Contains(err.Error(), "version mismatch") {
		t.Fatalf("NewListener() error = %v, want version mismatch", err)
	}
}

func TestNewListenerReregistersOnLeaseNotFound(t *testing.T) {
	t.Parallel()

	var registerCount atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case types.PathSDKDomain:
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[types.DomainResponse]{
				OK: true,
				Data: types.DomainResponse{
					RootHost:          "relay.example.com",
					SuggestedHostname: "demo.relay.example.com",
					Version:           types.SDKProtocolVersion,
				},
			})
		case types.PathSDKRegister:
			count := registerCount.Add(1)
			leaseID := "lease-1"
			if count > 1 {
				leaseID = "lease-2"
			}
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[types.RegisterResponse]{
				OK: true,
				Data: types.RegisterResponse{
					LeaseID: leaseID,
				},
			})
		case types.PathSDKRenew:
			var req types.RenewRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode renew request: %v", err)
			}
			if req.LeaseID == "lease-1" {
				writeSDKTestEnvelope(w, http.StatusNotFound, types.APIEnvelope[any]{
					OK:    false,
					Error: &types.APIError{Code: types.APIErrorCodeLeaseNotFound, Message: "lease not found"},
				})
				return
			}
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[types.RenewResponse]{
				OK:   true,
				Data: types.RenewResponse{LeaseID: req.LeaseID},
			})
		case types.PathSDKUnregister:
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[any]{OK: true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api, err := newRelayClient(server.URL, ListenerConfig{Name: "demo"})
	if err != nil {
		t.Fatalf("newRelayClient() error = %v", err)
	}

	listenerCtx, cancel := context.WithCancel(context.Background())
	listener := &Listener{
		ctx:              listenerCtx,
		cancel:           cancel,
		accepted:         make(chan net.Conn, 1),
		refill:           make(chan struct{}, 1),
		readyTarget:      0,
		leaseTTL:         defaultLeaseTTL,
		renewInterval:    10 * time.Millisecond,
		handshakeTimeout: defaultHandshakeTimeout,
		retryCount:       0,
		retryDelay:       10 * time.Millisecond,
		api:              api,
		state:            listenerStatePending,
	}
	go listener.run(listenerCtx)
	defer listener.Close()

	waitForSDKTest(t, func() bool {
		listener.mu.Lock()
		defer listener.mu.Unlock()
		return listener.leaseID == "lease-2"
	})
}

func TestExposeNoRelayInputs(t *testing.T) {
	t.Parallel()

	exposure, err := Expose(context.Background(), nil, "demo", types.LeaseMetadata{})
	if err != nil {
		t.Fatalf("Expose() error = %v", err)
	}
	if exposure != nil {
		t.Fatalf("Expose() exposure = %#v, want nil", exposure)
	}
}

func TestNormalizeRelayURLs(t *testing.T) {
	t.Parallel()

	got, err := NormalizeRelayURLs([]string{
		" localhost:4017 , https://relay.example.com/base/relay?x=1#frag ",
		"https://relay.example.com/base",
	})
	if err != nil {
		t.Fatalf("NormalizeRelayURLs() error = %v", err)
	}

	want := []string{
		"https://localhost:4017",
		"https://relay.example.com/base",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeRelayURLs() = %v, want %v", got, want)
	}
}

func newDomainServer(t *testing.T, version string) *httptest.Server {
	t.Helper()

	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != types.PathSDKDomain {
			http.NotFound(w, r)
			return
		}

		writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[types.DomainResponse]{
			OK: true,
			Data: types.DomainResponse{
				RootHost:          "relay.example.com",
				SuggestedHostname: "demo.relay.example.com",
				Version:           version,
			},
		})
	}))
}

func writeSDKTestEnvelope[T any](w http.ResponseWriter, status int, envelope types.APIEnvelope[T]) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope)
}

func waitForSDKTest(t *testing.T, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("timed out waiting for condition")
}
