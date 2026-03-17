package policy

import (
	"maps"
	"strings"
	"sync"
)

type BPSManager struct {
	leaseBPS map[string]int64
	mu       sync.RWMutex
}

func NewBPSManager() *BPSManager {
	return &BPSManager{
		leaseBPS: make(map[string]int64),
	}
}

func (m *BPSManager) LeaseBPS(leaseID string) int64 {
	if m == nil {
		return 0
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.leaseBPS[strings.TrimSpace(leaseID)]
}

func (m *BPSManager) SetLeaseBPS(leaseID string, bps int64) {
	if m == nil {
		return
	}

	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if bps <= 0 {
		delete(m.leaseBPS, leaseID)
		return
	}
	m.leaseBPS[leaseID] = bps
}

func (m *BPSManager) DeleteLeaseBPS(leaseID string) {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.leaseBPS, strings.TrimSpace(leaseID))
}

func (m *BPSManager) LeaseBPSLimits() map[string]int64 {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make(map[string]int64, len(m.leaseBPS))
	maps.Copy(out, m.leaseBPS)
	return out
}

func (m *BPSManager) SetLeaseBPSLimits(limits map[string]int64) {
	if m == nil {
		return
	}

	next := make(map[string]int64, len(limits))
	for leaseID, bps := range limits {
		leaseID = strings.TrimSpace(leaseID)
		if leaseID == "" || bps <= 0 {
			continue
		}
		next[leaseID] = bps
	}

	m.mu.Lock()
	m.leaseBPS = next
	m.mu.Unlock()
}
