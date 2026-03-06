package policy

import (
	"strings"
	"sync"
)

type Runtime struct {
	approver     *Approver
	ipFilter     *IPFilter
	bannedLeases map[string]struct{}
	mu           sync.RWMutex
}

func NewRuntime() *Runtime {
	return &Runtime{
		approver:     NewApprover(),
		ipFilter:     NewIPFilter(),
		bannedLeases: make(map[string]struct{}),
	}
}

func (r *Runtime) Approver() *Approver {
	if r == nil {
		return nil
	}
	return r.approver
}

func (r *Runtime) IPFilter() *IPFilter {
	if r == nil {
		return nil
	}
	return r.ipFilter
}

func (r *Runtime) BanLease(leaseID string) {
	if r == nil {
		return
	}
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bannedLeases[leaseID] = struct{}{}
}

func (r *Runtime) UnbanLease(leaseID string) {
	if r == nil {
		return
	}
	leaseID = strings.TrimSpace(leaseID)
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.bannedLeases, leaseID)
}

func (r *Runtime) IsLeaseBanned(leaseID string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.bannedLeases[strings.TrimSpace(leaseID)]
	return ok
}

func (r *Runtime) BannedLeases() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.bannedLeases))
	for leaseID := range r.bannedLeases {
		out = append(out, leaseID)
	}
	return out
}

func (r *Runtime) EffectiveApproval(leaseID string) bool {
	if r == nil || r.approver == nil {
		return true
	}
	if r.approver.Mode() == ModeAuto {
		return true
	}
	return r.approver.IsApproved(strings.TrimSpace(leaseID))
}

func (r *Runtime) IsLeaseDenied(leaseID string) bool {
	if r == nil || r.approver == nil {
		return false
	}
	return r.approver.IsDenied(strings.TrimSpace(leaseID))
}

func (r *Runtime) IsLeaseRoutable(leaseID string) bool {
	if r == nil {
		return true
	}
	if r.IsLeaseBanned(leaseID) || r.IsLeaseDenied(leaseID) {
		return false
	}
	return r.EffectiveApproval(leaseID)
}

func (r *Runtime) ForgetLease(leaseID string) {
	if r == nil || r.ipFilter == nil {
		return
	}
	r.ipFilter.RemoveLeaseIP(leaseID)
}
