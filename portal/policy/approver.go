package policy

import (
	"fmt"
	"sync"
)

type Mode string

const (
	ModeAuto   Mode = "auto"
	ModeManual Mode = "manual"
)

type Approver struct {
	approvedKeys map[string]struct{}
	deniedKeys   map[string]struct{}
	approvalMode Mode
	mu           sync.RWMutex
}

func NewApprover() *Approver {
	return &Approver{
		approvalMode: ModeAuto,
		approvedKeys: make(map[string]struct{}),
		deniedKeys:   make(map[string]struct{}),
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

func (a *Approver) IsApproved(key string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.approvedKeys[key]
	return ok
}

func (a *Approver) Approve(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.approvedKeys[key] = struct{}{}
	delete(a.deniedKeys, key)
}

func (a *Approver) Revoke(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.approvedKeys, key)
}

func (a *Approver) ApprovedKeys() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, 0, len(a.approvedKeys))
	for key := range a.approvedKeys {
		out = append(out, key)
	}
	return out
}

func (a *Approver) IsDenied(key string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.deniedKeys[key]
	return ok
}

func (a *Approver) Deny(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.deniedKeys[key] = struct{}{}
	delete(a.approvedKeys, key)
}

func (a *Approver) Undeny(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.deniedKeys, key)
}

func (a *Approver) DeniedKeys() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, 0, len(a.deniedKeys))
	for key := range a.deniedKeys {
		out = append(out, key)
	}
	return out
}

func (a *Approver) SetDecisions(approvedKeys, deniedKeys []string) {
	if a == nil {
		return
	}

	approved := make(map[string]struct{}, len(approvedKeys))
	for _, key := range approvedKeys {
		if key == "" {
			continue
		}
		approved[key] = struct{}{}
	}

	denied := make(map[string]struct{}, len(deniedKeys))
	for _, key := range deniedKeys {
		if key == "" {
			continue
		}
		delete(approved, key)
		denied[key] = struct{}{}
	}

	a.mu.Lock()
	a.approvedKeys = approved
	a.deniedKeys = denied
	a.mu.Unlock()
}
