package policy

import (
	"fmt"
	"strings"
	"sync"
)

type Mode string

const (
	ModeAuto   Mode = "auto"
	ModeManual Mode = "manual"
)

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

func (a *Approver) Mode() Mode {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.approvalMode
}

func (a *Approver) SetMode(mode Mode) error {
	if mode != ModeAuto && mode != ModeManual {
		return fmt.Errorf("invalid approval mode: %q", mode)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.approvalMode = mode
	return nil
}

func (a *Approver) IsApproved(leaseID string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.approvedLeases[leaseID]
	return ok
}

func (a *Approver) Approve(leaseID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.approvedLeases[leaseID] = struct{}{}
	delete(a.deniedLeases, leaseID)
}

func (a *Approver) Revoke(leaseID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.approvedLeases, leaseID)
}

func (a *Approver) ApprovedLeases() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, 0, len(a.approvedLeases))
	for leaseID := range a.approvedLeases {
		out = append(out, leaseID)
	}
	return out
}

func (a *Approver) IsDenied(leaseID string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.deniedLeases[leaseID]
	return ok
}

func (a *Approver) Deny(leaseID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.deniedLeases[leaseID] = struct{}{}
	delete(a.approvedLeases, leaseID)
}

func (a *Approver) Undeny(leaseID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.deniedLeases, leaseID)
}

func (a *Approver) DeniedLeases() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, 0, len(a.deniedLeases))
	for leaseID := range a.deniedLeases {
		out = append(out, leaseID)
	}
	return out
}

func (a *Approver) SetDecisions(approvedLeases, deniedLeases []string) {
	if a == nil {
		return
	}

	approved := make(map[string]struct{}, len(approvedLeases))
	for _, leaseID := range approvedLeases {
		leaseID = strings.TrimSpace(leaseID)
		if leaseID == "" {
			continue
		}
		approved[leaseID] = struct{}{}
	}

	denied := make(map[string]struct{}, len(deniedLeases))
	for _, leaseID := range deniedLeases {
		leaseID = strings.TrimSpace(leaseID)
		if leaseID == "" {
			continue
		}
		delete(approved, leaseID)
		denied[leaseID] = struct{}{}
	}

	a.mu.Lock()
	a.approvedLeases = approved
	a.deniedLeases = denied
	a.mu.Unlock()
}
