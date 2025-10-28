package relaydns

import (
	"sync"
	"time"

	"github.com/gosuda/relaydns/relaydns/core/proto/rdsec"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdverb"
)

type LeaseEntry struct {
	Lease        *rdverb.Lease
	Expires      time.Time
	LastSeen     time.Time
	ConnectionID int64 // Store the connection ID
}

type LeaseManager struct {
	leases      map[string]*LeaseEntry // Key: identity ID
	leasesLock  sync.RWMutex
	stopCh      chan struct{}
	ttlInterval time.Duration
}

func NewLeaseManager(ttlInterval time.Duration) *LeaseManager {
	return &LeaseManager{
		leases:      make(map[string]*LeaseEntry),
		stopCh:      make(chan struct{}),
		ttlInterval: ttlInterval,
	}
}

func (lm *LeaseManager) Start() {
	go lm.ttlWorker()
}

func (lm *LeaseManager) Stop() {
	close(lm.stopCh)
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
	defer lm.leasesLock.Unlock()

	now := time.Now()
	for id, lease := range lm.leases {
		if now.After(lease.Expires) {
			delete(lm.leases, id)
		}
	}
}

func (lm *LeaseManager) UpdateLease(lease *rdverb.Lease, connectionID int64) bool {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	identityID := string(lease.Identity.Id)
	expires := time.Unix(lease.Expires, 0)

	// Check if lease is already expired
	if time.Now().After(expires) {
		return false
	}

	lm.leases[identityID] = &LeaseEntry{
		Lease:        lease,
		Expires:      expires,
		LastSeen:     time.Now(),
		ConnectionID: connectionID,
	}

	return true
}

func (lm *LeaseManager) DeleteLease(identity *rdsec.Identity) bool {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	identityID := string(identity.Id)
	if _, exists := lm.leases[identityID]; exists {
		delete(lm.leases, identityID)
		return true
	}
	return false
}

func (lm *LeaseManager) GetLease(identity *rdsec.Identity) (*LeaseEntry, bool) {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()

	identityID := string(identity.Id)
	lease, exists := lm.leases[identityID]
	if !exists {
		return nil, false
	}

	// Check if lease is expired
	if time.Now().After(lease.Expires) {
		return nil, false
	}

	return lease, true
}

func (lm *LeaseManager) GetLeaseByID(leaseID string) (*LeaseEntry, bool) {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()

	lease, exists := lm.leases[leaseID]
	if !exists {
		return nil, false
	}

	// Check if lease is expired
	if time.Now().After(lease.Expires) {
		return nil, false
	}

	return lease, true
}

func (lm *LeaseManager) GetAllLeases() []*rdverb.Lease {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()

	now := time.Now()
	var validLeases []*rdverb.Lease

	for _, lease := range lm.leases {
		if now.Before(lease.Expires) {
			validLeases = append(validLeases, lease.Lease)
		}
	}

	return validLeases
}

func (lm *LeaseManager) CleanupLeasesByConnectionID(connectionID int64) []string {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	var cleanedLeaseIDs []string
	for leaseID, lease := range lm.leases {
		if lease.ConnectionID == connectionID {
			delete(lm.leases, leaseID)
			cleanedLeaseIDs = append(cleanedLeaseIDs, leaseID)
		}
	}

	return cleanedLeaseIDs
}
