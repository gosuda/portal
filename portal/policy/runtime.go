package policy

import (
	"sync"
)

type Runtime struct {
	approver           *Approver
	bpsManager         *BPSManager
	ipFilter           *IPFilter
	bannedIdentityKeys map[string]struct{}
	udpEnabled         bool
	udpMaxLeases       int
	tcpPortEnabled     bool
	tcpPortMaxLeases   int
	mu                 sync.RWMutex
}

func NewRuntime() *Runtime {
	return &Runtime{
		approver:           NewApprover(),
		bpsManager:         NewBPSManager(),
		ipFilter:           NewIPFilter(),
		bannedIdentityKeys: make(map[string]struct{}),
	}
}

func (r *Runtime) Approver() *Approver {
	return r.approver
}

func (r *Runtime) IPFilter() *IPFilter {
	return r.ipFilter
}

func (r *Runtime) BPSManager() *BPSManager {
	return r.bpsManager
}

func (r *Runtime) BanIdentity(key string) {
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bannedIdentityKeys[key] = struct{}{}
}

func (r *Runtime) UnbanIdentity(key string) {
	if key == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.bannedIdentityKeys, key)
}

func (r *Runtime) IsIdentityBanned(key string) bool {
	if key == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.bannedIdentityKeys[key]
	return ok
}

func (r *Runtime) BannedIdentityKeys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.bannedIdentityKeys))
	for key := range r.bannedIdentityKeys {
		out = append(out, key)
	}
	return out
}

func (r *Runtime) SetBannedIdentityKeys(keys []string) {
	bannedIdentityKeys := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		bannedIdentityKeys[key] = struct{}{}
	}

	r.mu.Lock()
	r.bannedIdentityKeys = bannedIdentityKeys
	r.mu.Unlock()
}

func (r *Runtime) EffectiveApproval(key string) bool {
	if r.approver == nil || key == "" {
		return true
	}
	if r.approver.Mode() == ModeAuto {
		return true
	}
	return r.approver.IsApproved(key)
}

func (r *Runtime) IsIdentityDenied(key string) bool {
	if r.approver == nil || key == "" {
		return false
	}
	return r.approver.IsDenied(key)
}

func (r *Runtime) IsIdentityRoutable(key string) bool {
	if r.IsIdentityBanned(key) || r.IsIdentityDenied(key) {
		return false
	}
	return r.EffectiveApproval(key)
}

func (r *Runtime) SetUDPPolicy(enabled bool, maxLeases int) {
	r.mu.Lock()
	r.udpEnabled = enabled
	r.udpMaxLeases = maxLeases
	r.mu.Unlock()
}

func (r *Runtime) IsUDPEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.udpEnabled
}

func (r *Runtime) UDPMaxLeases() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.udpMaxLeases
}

func (r *Runtime) SetTCPPortPolicy(enabled bool, maxLeases int) {
	r.mu.Lock()
	r.tcpPortEnabled = enabled
	r.tcpPortMaxLeases = maxLeases
	r.mu.Unlock()
}

func (r *Runtime) IsTCPPortEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tcpPortEnabled
}

func (r *Runtime) TCPPortMaxLeases() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tcpPortMaxLeases
}

func (r *Runtime) ForgetIdentity(key string) {
	if r.ipFilter != nil {
		r.ipFilter.RemoveIdentityIP(key)
	}
	if r.bpsManager != nil {
		r.bpsManager.DeleteIdentityBPS(key)
	}
}
