package policy

import (
	"fmt"
	"sync"
)

// Mode represents the approval mode for new connections.
type Mode string

const (
	ModeAuto   Mode = "auto"
	ModeManual Mode = "manual"
)

// Approver manages approval/denial state for leases.
type Approver struct {
	approvedLeases map[string]struct{}
	deniedLeases   map[string]struct{}
	approvalMode   Mode
	mu             sync.RWMutex
}

func NewApprover() *Approver {
	return &Approver{
		approvalMode:   ModeAuto,
		approvedLeases: make(map[string]struct{}),
		deniedLeases:   make(map[string]struct{}),
	}
}

func (m *Approver) GetApprovalMode() Mode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.approvalMode
}

func (m *Approver) SetApprovalMode(mode Mode) error {
	if mode != ModeAuto && mode != ModeManual {
		return fmt.Errorf("invalid approval mode: %q", mode)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approvalMode = mode
	return nil
}

func (m *Approver) IsLeaseApproved(leaseID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.approvedLeases[leaseID]
	return ok
}

func (m *Approver) ApproveLease(leaseID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approvedLeases[leaseID] = struct{}{}
	delete(m.deniedLeases, leaseID)
}

func (m *Approver) RevokeLease(leaseID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.approvedLeases, leaseID)
}

func (m *Approver) GetApprovedLeases() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, 0, len(m.approvedLeases))
	for id := range m.approvedLeases {
		result = append(result, id)
	}
	return result
}

func (m *Approver) IsLeaseDenied(leaseID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.deniedLeases[leaseID]
	return ok
}

func (m *Approver) DenyLease(leaseID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deniedLeases[leaseID] = struct{}{}
	delete(m.approvedLeases, leaseID)
}

func (m *Approver) UndenyLease(leaseID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.deniedLeases, leaseID)
}

func (m *Approver) GetDeniedLeases() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, 0, len(m.deniedLeases))
	for id := range m.deniedLeases {
		result = append(result, id)
	}
	return result
}
