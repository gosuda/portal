package policy

import (
	"maps"
	"sync"
)

type BPSManager struct {
	identityBPS map[string]int64
	mu          sync.RWMutex
}

func NewBPSManager() *BPSManager {
	return &BPSManager{
		identityBPS: make(map[string]int64),
	}
}

func (m *BPSManager) IdentityBPS(key string) int64 {
	if m == nil || key == "" {
		return 0
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.identityBPS[key]
}

func (m *BPSManager) SetIdentityBPS(key string, bps int64) {
	if m == nil || key == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if bps <= 0 {
		delete(m.identityBPS, key)
		return
	}
	m.identityBPS[key] = bps
}

func (m *BPSManager) DeleteIdentityBPS(key string) {
	if m == nil || key == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.identityBPS, key)
}

func (m *BPSManager) IdentityBPSLimits() map[string]int64 {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make(map[string]int64, len(m.identityBPS))
	maps.Copy(out, m.identityBPS)
	return out
}

func (m *BPSManager) SetIdentityBPSLimits(limits map[string]int64) {
	if m == nil {
		return
	}

	next := make(map[string]int64, len(limits))
	for key, bps := range limits {
		if key == "" || bps <= 0 {
			continue
		}
		next[key] = bps
	}

	m.mu.Lock()
	m.identityBPS = next
	m.mu.Unlock()
}
