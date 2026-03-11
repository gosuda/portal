package portal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/portal/policy"
)

func TestLeaseRegistryLifecycle(t *testing.T) {
	t.Parallel()

	runtime := policy.NewRuntime()
	registry := newLeaseRegistry(runtime)
	record := &leaseRecord{
		ID:           "lease_1",
		Hostnames:    []string{"demo.example.com"},
		ReverseToken: "tok_1",
		ExpiresAt:    time.Now().Add(30 * time.Second),
	}

	if err := registry.Register(record); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	lookedUp, ok := registry.Lookup("demo.example.com")
	if !ok || lookedUp != record {
		t.Fatalf("Lookup() = %v, %v, want registered lease", lookedUp, ok)
	}

	renewed, err := registry.Renew(record.ID, record.ReverseToken, time.Minute, "203.0.113.10")
	if err != nil {
		t.Fatalf("Renew() error = %v", err)
	}
	if renewed.ClientIP != "203.0.113.10" {
		t.Fatalf("Renew() client ip = %q, want %q", renewed.ClientIP, "203.0.113.10")
	}
	if got := runtime.IPFilter().LeaseIP(record.ID); got != "203.0.113.10" {
		t.Fatalf("Renew() did not register client IP for lease")
	}

	removed, err := registry.Unregister(record.ID, record.ReverseToken)
	if err != nil {
		t.Fatalf("Unregister() error = %v", err)
	}
	if removed != record {
		t.Fatalf("Unregister() record = %v, want original record", removed)
	}

	if _, ok := registry.Lookup("demo.example.com"); ok {
		t.Fatal("Lookup() after Unregister() = true, want false")
	}
	if got := runtime.IPFilter().LeaseIP(record.ID); got != "" {
		t.Fatalf("Unregister() lease IP = %q, want empty", got)
	}
}

func TestLeaseRegistryWildcardAndConflict(t *testing.T) {
	t.Parallel()

	registry := newLeaseRegistry(policy.NewRuntime())
	wildcardLease := &leaseRecord{
		ID:           "lease_wildcard",
		Hostnames:    []string{"*.example.com"},
		ReverseToken: "tok_wildcard",
		ExpiresAt:    time.Now().Add(30 * time.Second),
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
		ID:           "lease_conflict",
		Hostnames:    []string{"*.example.com"},
		ReverseToken: "tok_conflict",
		ExpiresAt:    time.Now().Add(30 * time.Second),
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
		ID:           "lease_policy",
		Name:         "demo",
		Hostnames:    []string{"demo.example.com"},
		ReverseToken: "tok_policy",
		ExpiresAt:    time.Now().Add(30 * time.Second),
		ClientIP:     "203.0.113.20",
		Broker:       newLeaseBroker("lease_policy", time.Minute, 1),
	}
	if err := registry.Register(record); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	if registry.IsRoutable(record) {
		t.Fatal("IsRoutable() = true, want false before approval")
	}

	snapshot := registry.Snapshot(record)
	if snapshot.IsApproved {
		t.Fatal("Snapshot().IsApproved = true, want false before approval")
	}
	if got := runtime.IPFilter().LeaseIP(record.ID); got != "203.0.113.20" {
		t.Fatalf("Register() lease IP = %q, want %q", got, "203.0.113.20")
	}

	runtime.Approver().Approve(record.ID)
	if !registry.IsRoutable(record) {
		t.Fatal("IsRoutable() = false, want true after approval")
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
		ID:           "lease_expired",
		Hostnames:    []string{"expired.example.com"},
		ReverseToken: "tok_expired",
		ExpiresAt:    time.Now().Add(-time.Second),
		Broker:       newLeaseBroker("lease_expired", time.Minute, 1),
	}
	if err := registry.Register(record); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	registry.cleanupExpired(time.Now())

	if _, ok := registry.Lookup("expired.example.com"); ok {
		t.Fatal("Lookup() after cleanupExpired() = true, want false")
	}
	if _, err := record.Broker.Claim(context.Background()); !errors.Is(err, errBrokerClosed) {
		t.Fatalf("Claim() after cleanupExpired() error = %v, want %v", err, errBrokerClosed)
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
