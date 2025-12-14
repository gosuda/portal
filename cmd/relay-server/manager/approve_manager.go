package manager

import "sync"

// ApprovalMode represents the approval mode for new connections.
type ApprovalMode string

const (
	ApprovalModeAuto   ApprovalMode = "auto"
	ApprovalModeManual ApprovalMode = "manual"
)

// ApproveManager manages approval/denial state for leases.
type ApproveManager struct {
	mu             sync.RWMutex
	approvalMode   ApprovalMode
	approvedLeases map[string]struct{}
	deniedLeases   map[string]struct{}
}

func NewApproveManager() *ApproveManager {
	return &ApproveManager{
		approvalMode:   ApprovalModeAuto,
		approvedLeases: make(map[string]struct{}),
		deniedLeases:   make(map[string]struct{}),
	}
}

func (m *ApproveManager) GetApprovalMode() ApprovalMode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.approvalMode
}

func (m *ApproveManager) SetApprovalMode(mode ApprovalMode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approvalMode = mode
}

func (m *ApproveManager) IsLeaseApproved(leaseID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.approvedLeases[leaseID]
	return ok
}

func (m *ApproveManager) ApproveLease(leaseID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approvedLeases[leaseID] = struct{}{}
	delete(m.deniedLeases, leaseID)
}

func (m *ApproveManager) RevokeLease(leaseID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.approvedLeases, leaseID)
}

func (m *ApproveManager) GetApprovedLeases() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, 0, len(m.approvedLeases))
	for id := range m.approvedLeases {
		result = append(result, id)
	}
	return result
}

func (m *ApproveManager) IsLeaseDenied(leaseID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.deniedLeases[leaseID]
	return ok
}

func (m *ApproveManager) DenyLease(leaseID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deniedLeases[leaseID] = struct{}{}
	delete(m.approvedLeases, leaseID)
}

func (m *ApproveManager) UndenyLease(leaseID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.deniedLeases, leaseID)
}

func (m *ApproveManager) GetDeniedLeases() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, 0, len(m.deniedLeases))
	for id := range m.deniedLeases {
		result = append(result, id)
	}
	return result
}
