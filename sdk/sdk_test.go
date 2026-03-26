package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

func TestNewListenerRegistersLeaseWithMainContract(t *testing.T) {
	registerReqCh := make(chan types.RegisterRequest, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case types.PathSDKDomain:
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[types.DomainResponse]{
				OK: true,
				Data: types.DomainResponse{
					SDKVersion: types.SDKProtocolVersion,
				},
			})
		case types.PathSDKRegister:
			var registerReq types.RegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&registerReq); err != nil {
				t.Fatalf("decode register request: %v", err)
			}
			select {
			case registerReqCh <- registerReq:
			default:
			}
			writeSDKTestEnvelope(w, http.StatusCreated, types.APIEnvelope[types.RegisterResponse]{
				OK: true,
				Data: types.RegisterResponse{
					LeaseID:  "lease-1",
					Hostname: "127.0.0.1",
					Metadata: registerReq.Metadata,
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
		Name:     "Demo-App",
		Metadata: types.LeaseMetadata{Owner: "alice"},
		LeaseTTL: 42 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewListener() error = %v", err)
	}
	defer listener.Close()

	var registerReq types.RegisterRequest
	waitForSDKTest(t, func() bool {
		select {
		case registerReq = <-registerReqCh:
			return true
		default:
			return false
		}
	})
	waitForSDKTest(t, func() bool {
		return listener.LeaseID() == "lease-1"
	})

	if registerReq.TTL != 42 {
		t.Fatalf("register request TTL = %d, want 42", registerReq.TTL)
	}
	if registerReq.UDPEnabled {
		t.Fatal("register request UDPEnabled = true, want false")
	}
	if registerReq.Name != "demo-app" {
		t.Fatalf("register request Name = %q, want %q", registerReq.Name, "demo-app")
	}
	if listener.LeaseID() != "lease-1" {
		t.Fatalf("LeaseID() = %q, want %q", listener.LeaseID(), "lease-1")
	}
	if got := listener.Hostname(); got != "127.0.0.1" {
		t.Fatalf("Hostname() = %q, want %q", got, "127.0.0.1")
	}
	if got := listener.PublicURL(); got != server.URL {
		t.Fatalf("PublicURL() = %q, want %q", got, server.URL)
	}
	if got := listener.Metadata(); got.Owner != "alice" {
		t.Fatalf("Metadata().Owner = %q, want %q", got.Owner, "alice")
	}
}

func TestExposeNoRelayInputs(t *testing.T) {
	exposure, err := Expose(context.Background(), ExposeConfig{Name: "demo"})
	if err != nil {
		t.Fatalf("Expose() error = %v", err)
	}
	if exposure == nil {
		t.Fatal("Expose() exposure = nil, want non-nil")
	}
	defer exposure.Close()
	if got := exposure.ActiveRelayURLs(); len(got) != 0 {
		t.Fatalf("Expose() relay urls = %v, want empty", got)
	}
}

func TestExposeResolvesOwnerPrivateKey(t *testing.T) {
	ownerPrivateKey := strings.Repeat("11", 32)
	identity, err := utils.ResolveSecp256k1Identity(ownerPrivateKey)
	if err != nil {
		t.Fatalf("ResolveSecp256k1Identity() error = %v", err)
	}

	registerReqCh := make(chan types.RegisterRequest, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case types.PathSDKDomain:
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[types.DomainResponse]{
				OK: true,
				Data: types.DomainResponse{
					SDKVersion: types.SDKProtocolVersion,
				},
			})
		case types.PathSDKRegister:
			var registerReq types.RegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&registerReq); err != nil {
				t.Fatalf("decode register request: %v", err)
			}
			select {
			case registerReqCh <- registerReq:
			default:
			}
			writeSDKTestEnvelope(w, http.StatusCreated, types.APIEnvelope[types.RegisterResponse]{
				OK: true,
				Data: types.RegisterResponse{
					LeaseID:  "lease-1",
					Hostname: "127.0.0.1",
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

	exposure, err := Expose(context.Background(), ExposeConfig{
		RelayURLs:       []string{server.URL},
		Name:            "demo",
		OwnerPrivateKey: ownerPrivateKey,
	})
	if err != nil {
		t.Fatalf("Expose() error = %v", err)
	}
	defer exposure.Close()

	var registerReq types.RegisterRequest
	waitForSDKTest(t, func() bool {
		select {
		case registerReq = <-registerReqCh:
			return true
		default:
			return false
		}
	})

	if registerReq.OwnerAddress != identity.Address {
		t.Fatalf("register request OwnerAddress = %q, want %q", registerReq.OwnerAddress, identity.Address)
	}
}

func TestExposeGeneratesOwnerAddressWithoutPrivateKey(t *testing.T) {
	registerReqCh := make(chan types.RegisterRequest, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case types.PathSDKDomain:
			writeSDKTestEnvelope(w, http.StatusOK, types.APIEnvelope[types.DomainResponse]{
				OK: true,
				Data: types.DomainResponse{
					SDKVersion: types.SDKProtocolVersion,
				},
			})
		case types.PathSDKRegister:
			var registerReq types.RegisterRequest
			if err := json.NewDecoder(r.Body).Decode(&registerReq); err != nil {
				t.Fatalf("decode register request: %v", err)
			}
			select {
			case registerReqCh <- registerReq:
			default:
			}
			writeSDKTestEnvelope(w, http.StatusCreated, types.APIEnvelope[types.RegisterResponse]{
				OK: true,
				Data: types.RegisterResponse{
					LeaseID:  "lease-1",
					Hostname: "127.0.0.1",
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

	exposure, err := Expose(context.Background(), ExposeConfig{
		RelayURLs: []string{server.URL},
		Name:      "demo",
	})
	if err != nil {
		t.Fatalf("Expose() error = %v", err)
	}
	defer exposure.Close()

	var registerReq types.RegisterRequest
	waitForSDKTest(t, func() bool {
		select {
		case registerReq = <-registerReqCh:
			return true
		default:
			return false
		}
	})

	if registerReq.OwnerAddress == "" {
		t.Fatal("register request OwnerAddress = empty, want generated address")
	}
	if _, err := utils.NormalizeEVMAddress(registerReq.OwnerAddress); err != nil {
		t.Fatalf("register request OwnerAddress = %q, want valid EVM address: %v", registerReq.OwnerAddress, err)
	}
}

func writeSDKTestEnvelope[T any](w http.ResponseWriter, status int, envelope types.APIEnvelope[T]) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope)
}

func waitForSDKTest(t *testing.T, fn func() bool) {
	t.Helper()

	waitForSDKTestWithTimeout(t, 5*time.Second, fn)
}

func waitForSDKTestWithTimeout(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("timed out waiting for condition")
}
