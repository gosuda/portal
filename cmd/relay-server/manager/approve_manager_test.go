package manager

import (
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewApproveManagerDefaults(t *testing.T) {
	t.Parallel()

	m := NewApproveManager()
	require.Equal(t, ApprovalModeAuto, m.GetApprovalMode(), "GetApprovalMode() mismatch")
	require.Empty(t, m.GetApprovedLeases(), "GetApprovedLeases() should be empty")
	require.Empty(t, m.GetDeniedLeases(), "GetDeniedLeases() should be empty")
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
			require.Equal(t, tt.mode, m.GetApprovalMode(), "GetApprovalMode() mismatch")
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

			require.Equal(t, tt.wantApproved, m.IsLeaseApproved(tt.leaseID), "IsLeaseApproved(%q) mismatch", tt.leaseID)
			require.Equal(t, tt.wantDenied, m.IsLeaseDenied(tt.leaseID), "IsLeaseDenied(%q) mismatch", tt.leaseID)
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
	require.Equal(t, wantApproved, approved, "GetApprovedLeases() mismatch")

	denied := m.GetDeniedLeases()
	slices.Sort(denied)
	wantDenied := []string{"lease-c", "lease-d"}
	require.Equal(t, wantDenied, denied, "GetDeniedLeases() mismatch")
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
	require.Len(t, leases, workers, "GetApprovedLeases() length mismatch")
	for i := range workers {
		leaseID := fmt.Sprintf("lease-%02d", i)
		require.True(t, m.IsLeaseApproved(leaseID), "lease %q should be approved", leaseID)
		require.False(t, m.IsLeaseDenied(leaseID), "lease %q should not be denied", leaseID)
	}
}
