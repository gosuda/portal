package manager

import (
	"fmt"
	"slices"
	"sync"
	"testing"
)

func TestNewApproveManagerDefaults(t *testing.T) {
	t.Parallel()

	m := NewApproveManager()
	if got := m.GetApprovalMode(); got != ApprovalModeAuto {
		t.Fatalf("GetApprovalMode() = %q, want %q", got, ApprovalModeAuto)
	}
	if got := m.GetApprovedLeases(); len(got) != 0 {
		t.Fatalf("GetApprovedLeases() len = %d, want 0", len(got))
	}
	if got := m.GetDeniedLeases(); len(got) != 0 {
		t.Fatalf("GetDeniedLeases() len = %d, want 0", len(got))
	}
}

func TestApproveManagerSetApprovalMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode ApprovalMode
	}{
		{name: "auto", mode: ApprovalModeAuto},
		{name: "manual", mode: ApprovalModeManual},
		{name: "custom", mode: ApprovalMode("custom")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := NewApproveManager()
			m.SetApprovalMode(tt.mode)
			if got := m.GetApprovalMode(); got != tt.mode {
				t.Fatalf("GetApprovalMode() = %q, want %q", got, tt.mode)
			}
		})
	}
}

func TestApproveManagerLeaseStateTransitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		leaseID      string
		setup        func(m *ApproveManager, leaseID string)
		wantApproved bool
		wantDenied   bool
	}{
		{
			name:         "approve marks approved",
			leaseID:      "lease-a",
			setup:        func(m *ApproveManager, leaseID string) { m.ApproveLease(leaseID) },
			wantApproved: true,
			wantDenied:   false,
		},
		{
			name:         "deny marks denied",
			leaseID:      "lease-a",
			setup:        func(m *ApproveManager, leaseID string) { m.DenyLease(leaseID) },
			wantApproved: false,
			wantDenied:   true,
		},
		{
			name:    "approve clears denied state",
			leaseID: "lease-a",
			setup: func(m *ApproveManager, leaseID string) {
				m.DenyLease(leaseID)
				m.ApproveLease(leaseID)
			},
			wantApproved: true,
			wantDenied:   false,
		},
		{
			name:    "deny clears approved state",
			leaseID: "lease-a",
			setup: func(m *ApproveManager, leaseID string) {
				m.ApproveLease(leaseID)
				m.DenyLease(leaseID)
			},
			wantApproved: false,
			wantDenied:   true,
		},
		{
			name:    "revoke removes approval",
			leaseID: "lease-a",
			setup: func(m *ApproveManager, leaseID string) {
				m.ApproveLease(leaseID)
				m.RevokeLease(leaseID)
			},
			wantApproved: false,
			wantDenied:   false,
		},
		{
			name:    "undeny removes denial",
			leaseID: "lease-a",
			setup: func(m *ApproveManager, leaseID string) {
				m.DenyLease(leaseID)
				m.UndenyLease(leaseID)
			},
			wantApproved: false,
			wantDenied:   false,
		},
		{
			name:         "empty lease ID is handled",
			leaseID:      "",
			setup:        func(m *ApproveManager, leaseID string) { m.ApproveLease(leaseID) },
			wantApproved: true,
			wantDenied:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := NewApproveManager()
			tt.setup(m, tt.leaseID)

			if got := m.IsLeaseApproved(tt.leaseID); got != tt.wantApproved {
				t.Fatalf("IsLeaseApproved(%q) = %v, want %v", tt.leaseID, got, tt.wantApproved)
			}
			if got := m.IsLeaseDenied(tt.leaseID); got != tt.wantDenied {
				t.Fatalf("IsLeaseDenied(%q) = %v, want %v", tt.leaseID, got, tt.wantDenied)
			}
		})
	}
}

func TestApproveManagerLeaseLists(t *testing.T) {
	t.Parallel()

	m := NewApproveManager()
	m.ApproveLease("lease-a")
	m.ApproveLease("lease-b")
	m.DenyLease("lease-c")
	m.DenyLease("lease-d")

	approved := m.GetApprovedLeases()
	slices.Sort(approved)
	wantApproved := []string{"lease-a", "lease-b"}
	if !slices.Equal(approved, wantApproved) {
		t.Fatalf("GetApprovedLeases() = %v, want %v", approved, wantApproved)
	}

	denied := m.GetDeniedLeases()
	slices.Sort(denied)
	wantDenied := []string{"lease-c", "lease-d"}
	if !slices.Equal(denied, wantDenied) {
		t.Fatalf("GetDeniedLeases() = %v, want %v", denied, wantDenied)
	}
}

func TestApproveManagerConcurrentApprovals(t *testing.T) {
	t.Parallel()

	const workers = 32
	m := NewApproveManager()

	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		leaseID := fmt.Sprintf("lease-%02d", i)
		go func(id string) {
			defer wg.Done()
			m.ApproveLease(id)
		}(leaseID)
	}
	wg.Wait()

	leases := m.GetApprovedLeases()
	if len(leases) != workers {
		t.Fatalf("GetApprovedLeases() len = %d, want %d", len(leases), workers)
	}
	for i := range workers {
		leaseID := fmt.Sprintf("lease-%02d", i)
		if !m.IsLeaseApproved(leaseID) {
			t.Fatalf("lease %q was not approved", leaseID)
		}
		if m.IsLeaseDenied(leaseID) {
			t.Fatalf("lease %q should not be denied", leaseID)
		}
	}
}
