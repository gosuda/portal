package portal

import (
	"testing"
	"time"

	"gosuda.org/portal/portal/sni"
	"gosuda.org/portal/types"
)

func newTestRegistryRelay(baseHost string) *RelayServer {
	s := &RelayServer{
		BaseHost:     baseHost,
		leaseManager: NewLeaseManager(DefaultLeaseTTL),
		reverseHub:   NewReverseHub(),
		sniRouter:    sni.NewRouter(":0"),
	}
	s.bindLeaseLifecycleHooks()
	return s
}

func newTestLease(id, name, token string) *types.Lease {
	return &types.Lease{
		ID:           id,
		Name:         name,
		ReverseToken: token,
		TLS:          true,
		Expires:      time.Now().Add(2 * time.Minute),
	}
}

func TestMatchLeaseToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		expected string
		provided string
		want     bool
	}{
		{name: "exact match", expected: "token-1", provided: "token-1", want: true},
		{name: "trimmed match", expected: " token-1 ", provided: "\ttoken-1\n", want: true},
		{name: "mismatch", expected: "token-1", provided: "token-2", want: false},
		{name: "empty expected", expected: "", provided: "token-1", want: false},
		{name: "empty provided", expected: "token-1", provided: " ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := matchLeaseToken(tt.expected, tt.provided); got != tt.want {
				t.Fatalf("matchLeaseToken(%q, %q)=%t, want %t", tt.expected, tt.provided, got, tt.want)
			}
		})
	}
}

func TestRegisterLease(t *testing.T) {
	t.Parallel()

	serv := newTestRegistryRelay("example.com")

	resp, apiErr := serv.RegisterLease(RegistryRegisterInput{
		LeaseID:      "lease-1",
		ReverseToken: "token-1",
		Name:         "demo",
		TLS:          true,
		PortalURL:    "https://portal.example.com",
	})
	if apiErr != nil {
		t.Fatalf("RegisterLease returned error: %+v", apiErr)
	}
	if !resp.Success {
		t.Fatal("expected success response")
	}
	if _, ok := serv.leaseManager.GetLeaseByID("lease-1"); !ok {
		t.Fatal("expected lease to be persisted")
	}
	sniName := types.BuildSNIName("demo", "example.com")
	if _, ok := serv.sniRouter.GetRoute(sniName); !ok {
		t.Fatalf("expected SNI route %q to be registered", sniName)
	}

	_, apiErr = serv.RegisterLease(RegistryRegisterInput{
		LeaseID:      "lease-2",
		ReverseToken: "token-2",
		Name:         "demo2",
		TLS:          false,
	})
	if apiErr == nil || apiErr.Code != "tls_required" {
		t.Fatalf("expected tls_required error, got %+v", apiErr)
	}
}

func TestRenewAndUnregisterLease(t *testing.T) {
	t.Parallel()

	serv := newTestRegistryRelay("example.com")
	lease := newTestLease("lease-1", "demo", "token-1")
	if !serv.leaseManager.UpdateLease(lease) {
		t.Fatal("failed to seed lease")
	}
	if err := serv.sniRouter.RegisterRoute(types.BuildSNIName("demo", "example.com"), "lease-1", "demo"); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	entry, _ := serv.leaseManager.GetLeaseByID("lease-1")
	oldExpires := entry.Lease.Expires

	if apiErr := serv.RenewLease(entry); apiErr != nil {
		t.Fatalf("RenewLease returned error: %+v", apiErr)
	}
	if entry.Lease.Expires.Equal(oldExpires) {
		t.Fatalf("renewed expiry did not change: %v", entry.Lease.Expires)
	}
	remaining := time.Until(entry.Lease.Expires)
	if remaining < 20*time.Second || remaining > 40*time.Second {
		t.Fatalf("renewed expiry remaining=%v, want around %v", remaining, DefaultLeaseTTL)
	}

	serv.UnregisterLease("lease-1")
	if _, ok := serv.leaseManager.GetLeaseByID("lease-1"); ok {
		t.Fatal("expected lease to be removed")
	}
	if _, ok := serv.sniRouter.GetRouteByLeaseID("lease-1"); ok {
		t.Fatal("expected SNI route to be removed")
	}
}

func TestRegistryDomain(t *testing.T) {
	t.Parallel()

	serv := newTestRegistryRelay("example.com")
	resp, apiErr := serv.RegistryDomain()
	if apiErr != nil {
		t.Fatalf("RegistryDomain returned error: %+v", apiErr)
	}
	if !resp.Success || resp.BaseDomain != "example.com" {
		t.Fatalf("unexpected domain response: %+v", resp)
	}

	serv.BaseHost = ""
	_, apiErr = serv.RegistryDomain()
	if apiErr == nil || apiErr.Code != "base_domain_missing" {
		t.Fatalf("expected base_domain_missing error, got %+v", apiErr)
	}
}
