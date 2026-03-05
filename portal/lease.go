package portal

import (
	"sync"
	"time"

	"gosuda.org/portal/types"
)

type LeaseManager struct {
	leases         map[string]*types.LeaseEntry
	stopCh         chan struct{}
	bannedLeases   map[string]struct{}
	onLeaseDeleted func(string)
	ttlInterval    time.Duration
	leasesLock     sync.RWMutex
	startOnce      sync.Once
	stopOnce       sync.Once
}

func NewLeaseManager(ttlInterval time.Duration) *LeaseManager {
	return &LeaseManager{
		leases:       make(map[string]*types.LeaseEntry),
		stopCh:       make(chan struct{}),
		ttlInterval:  ttlInterval,
		bannedLeases: make(map[string]struct{}),
	}
}

func (lm *LeaseManager) Start() {
	lm.startOnce.Do(func() {
		go lm.ttlWorker()
	})
}

func (lm *LeaseManager) Stop() {
	lm.stopOnce.Do(func() {
		close(lm.stopCh)
	})
}

func (lm *LeaseManager) ttlWorker() {
	ticker := time.NewTicker(lm.ttlInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			lm.cleanupExpiredLeases()
		case <-lm.stopCh:
			return
		}
	}
}

func (lm *LeaseManager) cleanupExpiredLeases() {
	lm.leasesLock.Lock()

	now := time.Now()
	expired := make([]string, 0)
	for id, lease := range lm.leases {
		if now.After(lease.Lease.Expires) {
			delete(lm.leases, id)
			expired = append(expired, id)
		}
	}
	callback := lm.onLeaseDeleted
	lm.leasesLock.Unlock()

	if callback == nil {
		return
	}
	for _, id := range expired {
		callback(id)
	}
}

func (lm *LeaseManager) UpdateLease(lease *types.Lease) bool {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	identityID := lease.ID

	// Check if lease is already expired
	if time.Now().After(lease.Expires) {
		return false
	}

	// policy checks
	if _, banned := lm.bannedLeases[identityID]; banned {
		return false
	}

	// Check for name conflicts (only if name is not empty)
	if lease.Name != "" && lease.Name != "(unnamed)" {
		for existingID, existingEntry := range lm.leases {
			// Skip if it's the same identity (updating own lease)
			if existingID == identityID {
				continue
			}
			// Check if another identity is using the same name
			if existingEntry.Lease.Name == lease.Name {
				// Name conflict with a different identity
				return false
			}
		}
	}

	var firstSeen time.Time
	if existing, exists := lm.leases[identityID]; exists {
		firstSeen = existing.FirstSeen
	}
	if firstSeen.IsZero() {
		firstSeen = time.Now()
	}

	lm.leases[identityID] = &types.LeaseEntry{
		Lease:     lease,
		LastSeen:  time.Now(),
		FirstSeen: firstSeen,
	}

	return true
}

func (lm *LeaseManager) DeleteLease(leaseID string) bool {
	lm.leasesLock.Lock()
	if _, exists := lm.leases[leaseID]; exists {
		delete(lm.leases, leaseID)
		callback := lm.onLeaseDeleted
		lm.leasesLock.Unlock()
		if callback != nil {
			callback(leaseID)
		}
		return true
	}
	lm.leasesLock.Unlock()
	return false
}

func (lm *LeaseManager) SetOnLeaseDeleted(callback func(string)) {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()
	lm.onLeaseDeleted = callback
}

func (lm *LeaseManager) GetLeaseByID(leaseID string) (*types.LeaseEntry, bool) {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()

	// Check if banned
	if _, banned := lm.bannedLeases[leaseID]; banned {
		return nil, false
	}

	lease, exists := lm.leases[leaseID]
	if !exists {
		return nil, false
	}

	// Check if lease is expired
	if time.Now().After(lease.Lease.Expires) {
		return nil, false
	}

	return lease, true
}

// GetAllLeaseEntries returns all lease entries from the lease manager.
func (lm *LeaseManager) GetAllLeaseEntries() []*types.LeaseEntry {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()

	now := time.Now()
	var entries []*types.LeaseEntry

	for _, entry := range lm.leases {
		if now.Before(entry.Lease.Expires) {
			entries = append(entries, entry)
		}
	}

	return entries
}

// BanLease adds a lease ID to the denylist.
func (lm *LeaseManager) BanLease(leaseID string) {
	lm.leasesLock.Lock()
	lm.bannedLeases[leaseID] = struct{}{}
	lm.leasesLock.Unlock()
}

func (lm *LeaseManager) UnbanLease(leaseID string) {
	lm.leasesLock.Lock()
	delete(lm.bannedLeases, leaseID)
	lm.leasesLock.Unlock()
}

func (lm *LeaseManager) GetBannedLeases() []string {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()
	banned := make([]string, 0, len(lm.bannedLeases))
	for id := range lm.bannedLeases {
		banned = append(banned, id)
	}
	return banned
}
