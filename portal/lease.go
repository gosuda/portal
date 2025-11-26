package portal

import (
	"regexp"
	"sync"
	"time"

	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
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

	// policy controls
	bannedLeases map[string]struct{}
	namePattern  *regexp.Regexp
	minTTL       time.Duration // 0 = no bound
	maxTTL       time.Duration // 0 = no bound
	// per-lease byte limit
	bpsLimits  map[string]int64 // leaseID -> bytes-per-second (0 = unlimited)
	defaultBPS int64            // default bytes-per-second for new/updated leases (0 = none)
}

func NewLeaseManager(ttlInterval time.Duration) *LeaseManager {
	return &LeaseManager{
		leases:       make(map[string]*LeaseEntry),
		stopCh:       make(chan struct{}),
		ttlInterval:  ttlInterval,
		bannedLeases: make(map[string]struct{}),
		bpsLimits:    make(map[string]int64),
		defaultBPS:   0,
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
			delete(lm.bpsLimits, id)
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

	// policy checks
	if _, banned := lm.bannedLeases[identityID]; banned {
		return false
	}
	if lm.namePattern != nil && lease.Name != "" && !lm.namePattern.MatchString(lease.Name) {
		return false
	}
	// reserved prefix check removed
	if lm.minTTL > 0 || lm.maxTTL > 0 {
		ttl := time.Until(expires)
		if lm.minTTL > 0 && ttl < lm.minTTL {
			return false
		}
		if lm.maxTTL > 0 && ttl > lm.maxTTL {
			return false
		}
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

	lm.leases[identityID] = &LeaseEntry{
		Lease:        lease,
		Expires:      expires,
		LastSeen:     time.Now(),
		ConnectionID: connectionID,
	}

	// Apply default BPS limit for this lease if configured and no explicit limit set
	if lm.defaultBPS > 0 {
		if _, exists := lm.bpsLimits[identityID]; !exists {
			lm.bpsLimits[identityID] = lm.defaultBPS
		}
	}

	return true
}

func (lm *LeaseManager) DeleteLease(identity *rdsec.Identity) bool {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	identityID := string(identity.Id)
	if _, exists := lm.leases[identityID]; exists {
		delete(lm.leases, identityID)
		delete(lm.bpsLimits, identityID)
		return true
	}
	return false
}

func (lm *LeaseManager) GetLease(identity *rdsec.Identity) (*LeaseEntry, bool) {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()

	identityID := string(identity.Id)
	
	// Check if banned
	if _, banned := lm.bannedLeases[identityID]; banned {
		return nil, false
	}

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

	// Check if banned
	if _, banned := lm.bannedLeases[leaseID]; banned {
		return nil, false
	}

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

// Lease policy configuration helpers
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

func (lm *LeaseManager) GetBannedLeases() [][]byte {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()
	banned := make([][]byte, 0, len(lm.bannedLeases))
	for id := range lm.bannedLeases {
		banned = append(banned, []byte(id))
	}
	return banned
}

func (lm *LeaseManager) SetNamePattern(pattern string) error {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()
	if pattern == "" {
		lm.namePattern = nil
		return nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	lm.namePattern = re
	return nil
}

// SetReservedPrefixes removed: reserved prefix policy no longer supported

func (lm *LeaseManager) SetTTLBounds(min, max time.Duration) {
	lm.leasesLock.Lock()
	lm.minTTL = min
	lm.maxTTL = max
	lm.leasesLock.Unlock()
}

func (lm *LeaseManager) CleanupLeasesByConnectionID(connectionID int64) []string {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	var cleanedLeaseIDs []string
	for leaseID, lease := range lm.leases {
		if lease.ConnectionID == connectionID {
			delete(lm.leases, leaseID)
			delete(lm.bpsLimits, leaseID)
			cleanedLeaseIDs = append(cleanedLeaseIDs, leaseID)
		}
	}

	return cleanedLeaseIDs
}

// Per-lease BPS limit configuration
func (lm *LeaseManager) SetBPSLimit(leaseID string, bps int64) {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()
	if bps <= 0 {
		delete(lm.bpsLimits, leaseID)
		return
	}
	lm.bpsLimits[leaseID] = bps
}

func (lm *LeaseManager) GetBPSLimit(leaseID string) int64 {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()
	if v, ok := lm.bpsLimits[leaseID]; ok {
		return v
	}
	return 0
}

// SetDefaultBPS sets a default bytes-per-second limit applied to leases on update/registration
// If set to 0, no default is applied. Existing explicit per-lease limits are not overwritten.
func (lm *LeaseManager) SetDefaultBPS(bps int64) {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()
	if bps < 0 {
		bps = 0
	}
	lm.defaultBPS = bps
}
