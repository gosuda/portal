package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/types"
)

func TestNewListenerFailsFastOnRegisterError(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case types.PathSDKDomain:
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[types.DomainResponse]{
				OK: true,
				Data: types.DomainResponse{
					RootHost: "localhost",
					Version:  types.SDKProtocolVersion,
				},
			})
		case types.PathSDKRegister:
			writeSDKTestEnvelope(w, http.StatusConflict, types.APIEnvelope[any]{
				OK:    false,
				Error: &types.APIError{Code: types.APIErrorCodeHostnameConflict, Message: "hostname already registered"},
			})
		default:
			http.NotFound(w, r)
			return
		}
	}))
	defer server.Close()

	listener, err := NewListener(context.Background(), server.URL, ListenerConfig{Name: "demo"})
	if err == nil {
		t.Fatal("NewListener() error = nil, want register failure")
	}
	if listener != nil {
		t.Fatalf("NewListener() listener = %#v, want nil", listener)
	}
}

func TestNewListenerRegistersLeaseWithMainContract(t *testing.T) {
	t.Parallel()

	var registerReq types.RegisterRequest
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case types.PathSDKDomain:
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[types.DomainResponse]{
				OK: true,
				Data: types.DomainResponse{
					RootHost: "localhost",
					Version:  types.SDKProtocolVersion,
				},
			})
		case types.PathSDKRegister:
			if err := json.NewDecoder(r.Body).Decode(&registerReq); err != nil {
				t.Fatalf("decode register request: %v", err)
			}
			writeSDKTestEnvelope(w, http.StatusCreated, types.APIEnvelope[types.RegisterResponse]{
				OK: true,
				Data: types.RegisterResponse{
					LeaseID:   "lease-1",
					Hostnames: []string{"127.0.0.1"},
					Metadata:  registerReq.Metadata,
				},
			})
		case types.PathSDKConnect:
			writeSDKTestEnvelope(w, http.StatusForbidden, types.APIEnvelope[any]{
				OK:    false,
				Error: &types.APIError{Code: types.APIErrorCodeUnauthorized, Message: "not used in test"},
			})
		case types.PathSDKRenew:
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[types.RenewResponse]{
				OK:   true,
				Data: types.RenewResponse{LeaseID: "lease-1"},
			})
		case types.PathSDKUnregister:
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[any]{OK: true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	listener, err := NewListener(context.Background(), server.URL, ListenerConfig{
		Name:     "demo",
		Metadata: types.LeaseMetadata{Owner: "alice"},
		LeaseTTL: 42 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewListener() error = %v", err)
	}
	defer listener.Close()

	if registerReq.TTL != 42 {
		t.Fatalf("register request TTL = %d, want 42", registerReq.TTL)
	}
	if listener.LeaseID() != "lease-1" {
		t.Fatalf("LeaseID() = %q, want %q", listener.LeaseID(), "lease-1")
	}
	if got := listener.Hostnames(); !reflect.DeepEqual(got, []string{"127.0.0.1"}) {
		t.Fatalf("Hostnames() = %v, want %v", got, []string{"127.0.0.1"})
	}
	if got := listener.PublicURLs(); !reflect.DeepEqual(got, []string{"https://127.0.0.1"}) {
		t.Fatalf("PublicURLs() = %v, want %v", got, []string{"https://127.0.0.1"})
	}
	if got := listener.Metadata(); got.Owner != "alice" {
		t.Fatalf("Metadata().Owner = %q, want %q", got.Owner, "alice")
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
					RootHost: "localhost",
					Version:  types.SDKProtocolVersion,
				},
			})
		case types.PathSDKRegister:
			count := registerCount.Add(1)
			leaseID := "lease-1"
			if count > 1 {
				leaseID = "lease-2"
			}
			writeSDKTestEnvelope(w, http.StatusCreated, types.APIEnvelope[types.RegisterResponse]{
				OK: true,
				Data: types.RegisterResponse{
					LeaseID:   leaseID,
					Hostnames: []string{"127.0.0.1"},
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
		case types.PathSDKConnect:
			writeSDKTestEnvelope(w, http.StatusForbidden, types.APIEnvelope[any]{
				OK:    false,
				Error: &types.APIError{Code: types.APIErrorCodeUnauthorized, Message: "not used in test"},
			})
		case types.PathSDKUnregister:
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[any]{OK: true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	listener, err := NewListener(context.Background(), server.URL, ListenerConfig{
		Name:        "demo",
		LeaseTTL:    80 * time.Millisecond,
		RenewBefore: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewListener() error = %v", err)
	}
	defer listener.Close()

	waitForSDKTest(t, func() bool {
		return listener.LeaseID() == "lease-2"
	})
}

func TestExposeFailsFastWhenAnyRelayCannotRegister(t *testing.T) {
	t.Parallel()

	var unregisterCount atomic.Int32
	goodServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case types.PathSDKDomain:
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[types.DomainResponse]{
				OK: true,
				Data: types.DomainResponse{
					RootHost: "localhost",
					Version:  types.SDKProtocolVersion,
				},
			})
		case types.PathSDKRegister:
			writeSDKTestEnvelope(w, http.StatusCreated, types.APIEnvelope[types.RegisterResponse]{
				OK: true,
				Data: types.RegisterResponse{
					LeaseID:   "lease-good",
					Hostnames: []string{"127.0.0.1"},
				},
			})
		case types.PathSDKConnect:
			writeSDKTestEnvelope(w, http.StatusForbidden, types.APIEnvelope[any]{
				OK:    false,
				Error: &types.APIError{Code: types.APIErrorCodeUnauthorized, Message: "not used in test"},
			})
		case types.PathSDKRenew:
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[types.RenewResponse]{
				OK:   true,
				Data: types.RenewResponse{LeaseID: "lease-good"},
			})
		case types.PathSDKUnregister:
			unregisterCount.Add(1)
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[any]{OK: true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer goodServer.Close()

	badServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case types.PathSDKDomain:
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[types.DomainResponse]{
				OK: true,
				Data: types.DomainResponse{
					RootHost: "localhost",
					Version:  types.SDKProtocolVersion,
				},
			})
		case types.PathSDKRegister:
			writeSDKTestEnvelope(w, http.StatusConflict, types.APIEnvelope[any]{
				OK:    false,
				Error: &types.APIError{Code: types.APIErrorCodeHostnameConflict, Message: "hostname already registered"},
			})
		default:
			http.NotFound(w, r)
			return
		}
	}))
	defer badServer.Close()

	exposure, err := Expose(context.Background(), []string{goodServer.URL, badServer.URL}, "demo", types.LeaseMetadata{})
	if err == nil {
		t.Fatal("Expose() error = nil, want register failure")
	}
	if exposure != nil {
		t.Fatalf("Expose() exposure = %#v, want nil", exposure)
	}

	waitForSDKTest(t, func() bool {
		return unregisterCount.Load() > 0
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
