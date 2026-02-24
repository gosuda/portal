package portal

import (
	"slices"
	"testing"
	"time"
)

func TestLeaseManagerDeleteLeaseInvokesCallback(t *testing.T) {
	lm := NewLeaseManager(time.Second)

	var deleted []string
	lm.SetOnLeaseDeleted(func(id string) {
		deleted = append(deleted, id)
	})

	lease := &Lease{
		ID:      "lease-1",
		Name:    "app-1",
		Address: "127.0.0.1:10001",
		Expires: time.Now().Add(30 * time.Second),
	}
	if !lm.UpdateLease(lease) {
		t.Fatalf("expected lease update success")
	}

	if !lm.DeleteLease("lease-1") {
		t.Fatalf("expected lease deletion success")
	}
	if !slices.Contains(deleted, "lease-1") {
		t.Fatalf("expected callback with lease-1, got %v", deleted)
	}
}

func TestLeaseManagerCleanupExpiredLeasesInvokesCallback(t *testing.T) {
	lm := NewLeaseManager(time.Second)

	var deleted []string
	lm.SetOnLeaseDeleted(func(id string) {
		deleted = append(deleted, id)
	})

	lm.leases["expired-1"] = &LeaseEntry{
		Lease: &Lease{
			ID:      "expired-1",
			Name:    "expired",
			Address: "127.0.0.1:10002",
			Expires: time.Now().Add(-1 * time.Second),
		},
		Expires: time.Now().Add(-1 * time.Second),
	}
	lm.leases["active-1"] = &LeaseEntry{
		Lease: &Lease{
			ID:      "active-1",
			Name:    "active",
			Address: "127.0.0.1:10003",
			Expires: time.Now().Add(30 * time.Second),
		},
		Expires: time.Now().Add(30 * time.Second),
	}

	lm.cleanupExpiredLeases()

	if !slices.Contains(deleted, "expired-1") {
		t.Fatalf("expected callback with expired-1, got %v", deleted)
	}
	if _, ok := lm.leases["expired-1"]; ok {
		t.Fatal("expected expired-1 removed")
	}
	if _, ok := lm.leases["active-1"]; !ok {
		t.Fatal("expected active-1 to remain")
	}
}
