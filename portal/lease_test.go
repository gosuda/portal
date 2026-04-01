package portal

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/portal/transport"
	"github.com/gosuda/portal/v2/types"
)

func TestLeaseRegistryLifecycle(t *testing.T) {
	t.Parallel()

	runtime := policy.NewRuntime()
	registry := newLeaseRegistry(runtime)
	record := &leaseRecord{
		Lease: types.Lease{
			Identity: types.Identity{
				Name:    "demo",
				Address: "addr-1",
			},
			Hostname:  "demo.example.com",
			ExpiresAt: time.Now().Add(30 * time.Second),
		},
		stream: transport.NewRelayStream("addr-1", time.Minute, 1),
	}

	if err := registry.Register(record); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	lookedUp, ok := registry.Lookup("demo.example.com")
	if !ok || lookedUp != record {
		t.Fatalf("Lookup() = %v, %v, want registered lease", lookedUp, ok)
	}

	renewed, err := registry.Renew(record.Copy(), time.Minute, "203.0.113.10", "")
	if err != nil {
		t.Fatalf("Renew() error = %v", err)
	}
	if renewed.ClientIP != "203.0.113.10" {
		t.Fatalf("Renew() client ip = %q, want %q", renewed.ClientIP, "203.0.113.10")
	}
	if got := runtime.IPFilter().IdentityIP(record.Key()); got != "203.0.113.10" {
		t.Fatalf("Renew() did not register client IP for lease")
	}

	removed, err := registry.Unregister(record.Copy())
	if err != nil {
		t.Fatalf("Unregister() error = %v", err)
	}
	if removed != record {
		t.Fatalf("Unregister() record = %v, want original record", removed)
	}

	if _, ok := registry.Lookup("demo.example.com"); ok {
		t.Fatal("Lookup() after Unregister() = true, want false")
	}
	if got := runtime.IPFilter().IdentityIP(record.Key()); got != "" {
		t.Fatalf("Unregister() lease IP = %q, want empty", got)
	}
}

func TestLeaseRegistryWildcardAndConflict(t *testing.T) {
	t.Parallel()

	registry := newLeaseRegistry(policy.NewRuntime())
	wildcardLease := &leaseRecord{
		Lease: types.Lease{
			Identity: types.Identity{
				Name:    "wildcard",
				Address: "addr-wildcard",
			},
			Hostname:  "*.example.com",
			ExpiresAt: time.Now().Add(30 * time.Second),
		},
		stream: transport.NewRelayStream("addr-wildcard", time.Minute, 1),
	}
	if err := registry.Register(wildcardLease); err != nil {
		t.Fatalf("Register(wildcard) error = %v", err)
	}

	if _, ok := registry.Lookup("app.example.com"); !ok {
		t.Fatal("Lookup(one-level wildcard) = false, want true")
	}
	if _, ok := registry.Lookup("deep.app.example.com"); ok {
		t.Fatal("Lookup(multi-level wildcard) = true, want false")
	}

	conflict := &leaseRecord{
		Lease: types.Lease{
			Identity: types.Identity{
				Name:    "conflict",
				Address: "addr-conflict",
			},
			Hostname:  "*.example.com",
			ExpiresAt: time.Now().Add(30 * time.Second),
		},
		stream: transport.NewRelayStream("addr-conflict", time.Minute, 1),
	}
	err := registry.Register(conflict)
	if !errors.Is(err, errHostnameConflict) {
		t.Fatalf("Register(conflict) error = %v, want hostname conflict", err)
	}
}

func TestLeaseRegistrySnapshotAndRoutableUsePolicy(t *testing.T) {
	t.Parallel()

	runtime := policy.NewRuntime()
	if err := runtime.Approver().SetMode(policy.ModeManual); err != nil {
		t.Fatalf("SetMode() error = %v", err)
	}

	registry := newLeaseRegistry(runtime)
	record := &leaseRecord{
		Lease: types.Lease{
			Identity: types.Identity{
				Name:    "demo",
				Address: "addr-policy",
			},
			Hostname:  "demo.example.com",
			ExpiresAt: time.Now().Add(30 * time.Second),
			ClientIP:  "203.0.113.20",
		},
		stream: transport.NewRelayStream("addr-policy", time.Minute, 1),
	}
	if err := registry.Register(record); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if registry.policy.IsIdentityRoutable(record.Key()) {
		t.Fatal("policy.IsIdentityRoutable() = true, want false before approval")
	}

	snapshot := registry.Snapshot(record)
	if snapshot.IsApproved {
		t.Fatal("Snapshot().IsApproved = true, want false before approval")
	}
	if got := runtime.IPFilter().IdentityIP(record.Key()); got != "203.0.113.20" {
		t.Fatalf("Register() lease IP = %q, want %q", got, "203.0.113.20")
	}

	runtime.Approver().Approve(record.Key())
	if !registry.policy.IsIdentityRoutable(record.Key()) {
		t.Fatal("policy.IsIdentityRoutable() = false, want true after approval")
	}

	snapshot = registry.Snapshot(record)
	if !snapshot.IsApproved {
		t.Fatal("Snapshot().IsApproved = false, want true after approval")
	}
}

func TestLeaseRegistryCleanupExpiredClosesBroker(t *testing.T) {
	t.Parallel()

	registry := newLeaseRegistry(policy.NewRuntime())
	record := &leaseRecord{
		Lease: types.Lease{
			Identity: types.Identity{
				Name:    "expired",
				Address: "addr-expired",
			},
			Hostname:  "expired.example.com",
			ExpiresAt: time.Now().Add(-time.Second),
		},
		stream: transport.NewRelayStream("addr-expired", time.Minute, 1),
	}
	if err := registry.Register(record); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	for _, lease := range registry.removeExpired(time.Now()) {
		lease.Close()
	}

	if _, ok := registry.Lookup("expired.example.com"); ok {
		t.Fatal("Lookup() after removeExpired() = true, want false")
	}
	if _, err := record.stream.Claim(context.Background()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Claim() after removeExpired() error = %v, want %v", err, net.ErrClosed)
	}
}

func TestLeaseRegistryRunJanitorRejectsNonPositiveInterval(t *testing.T) {
	t.Parallel()

	registry := newLeaseRegistry(policy.NewRuntime())
	err := registry.RunJanitor(context.Background(), 0)
	if err == nil {
		t.Fatal("RunJanitor() error = nil, want validation error")
	}
}
