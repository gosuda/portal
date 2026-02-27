package manager

import (
	"sync"
	"sync/atomic"
)

// ConnLimitManager manages per-lease concurrent connection limits.
type ConnLimitManager struct {
	mu           sync.Mutex
	limits       map[string]int64         // leaseID → max concurrent connections (0 = use default)
	active       map[string]*atomic.Int64 // leaseID → current active count
	defaultLimit int64                    // server-wide default (0 = unlimited)
}

// NewConnLimitManager creates a new connection limit manager.
func NewConnLimitManager() *ConnLimitManager {
	return &ConnLimitManager{
		limits: make(map[string]int64),
		active: make(map[string]*atomic.Int64),
	}
}

// SetDefaultLimit sets the server-wide default connection limit.
func (m *ConnLimitManager) SetDefaultLimit(n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n < 0 {
		n = 0
	}
	m.defaultLimit = n
}

// GetDefaultLimit returns the server-wide default connection limit.
func (m *ConnLimitManager) GetDefaultLimit() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defaultLimit
}

// SetLimit sets a per-lease connection limit override. Pass 0 to remove the override.
func (m *ConnLimitManager) SetLimit(leaseID string, n int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n <= 0 {
		delete(m.limits, leaseID)
		return
	}
	m.limits[leaseID] = n
}

// GetLimit returns the effective connection limit for a lease (per-lease → default fallback).
// Returns 0 if unlimited.
func (m *ConnLimitManager) GetLimit(leaseID string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.limits[leaseID]; ok {
		return v
	}
	return m.defaultLimit
}

// GetAllLimits returns a copy of all per-lease limit overrides (for settings persistence).
func (m *ConnLimitManager) GetAllLimits() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]int64, len(m.limits))
	for k, v := range m.limits {
		result[k] = v
	}
	return result
}

// TryAcquire atomically increments the active count for a lease if under the limit.
// Returns true if the connection is allowed, false if the limit is reached.
func (m *ConnLimitManager) TryAcquire(leaseID string) bool {
	m.mu.Lock()
	limit := m.effectiveLimit(leaseID)
	if limit <= 0 {
		// Unlimited — still track active count for observability.
		counter := m.getOrCreateCounter(leaseID)
		m.mu.Unlock()
		counter.Add(1)
		return true
	}

	counter := m.getOrCreateCounter(leaseID)
	// Optimistic check under lock to prevent races between limit lookup and CAS.
	current := counter.Load()
	if current >= limit {
		m.mu.Unlock()
		return false
	}
	counter.Add(1)
	m.mu.Unlock()
	return true
}

// Release decrements the active connection count for a lease.
func (m *ConnLimitManager) Release(leaseID string) {
	m.mu.Lock()
	counter, ok := m.active[leaseID]
	m.mu.Unlock()
	if ok {
		if counter.Add(-1) < 0 {
			counter.Store(0) // safety floor
		}
	}
}

// ActiveCount returns the current active connection count for a lease.
func (m *ConnLimitManager) ActiveCount(leaseID string) int64 {
	m.mu.Lock()
	counter, ok := m.active[leaseID]
	m.mu.Unlock()
	if ok {
		return counter.Load()
	}
	return 0
}

// CleanupLease removes all state for a deleted lease.
func (m *ConnLimitManager) CleanupLease(leaseID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.limits, leaseID)
	delete(m.active, leaseID)
}

// effectiveLimit returns the limit for a lease (must hold mu).
func (m *ConnLimitManager) effectiveLimit(leaseID string) int64 {
	if v, ok := m.limits[leaseID]; ok {
		return v
	}
	return m.defaultLimit
}

// getOrCreateCounter returns the atomic counter for a lease, creating if needed (must hold mu).
func (m *ConnLimitManager) getOrCreateCounter(leaseID string) *atomic.Int64 {
	if c, ok := m.active[leaseID]; ok {
		return c
	}
	c := &atomic.Int64{}
	m.active[leaseID] = c
	return c
}
