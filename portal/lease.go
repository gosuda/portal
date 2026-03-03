package portal

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"

	"gosuda.org/portal/types"
)

// Lease represents a registered service.
type Lease struct {
	Expires      time.Time      `json:"expires"`
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	ReverseToken string         `json:"-"`
	Metadata     types.Metadata `json:"metadata"`
	TLS          bool           `json:"tls"`
}

// LeaseEntry represents a registered lease with expiration tracking.
type LeaseEntry struct {
	Lease          *Lease
	Expires        time.Time
	LastSeen       time.Time
	FirstSeen      time.Time
	ParsedMetadata *types.ParsedMetadata // Cached parsed metadata
}

type LeaseManager struct {
	leases         map[string]*LeaseEntry
	stopCh         chan struct{}
	bannedLeases   map[string]struct{}
	namePattern    *regexp.Regexp
	onLeaseDeleted func(string)
	ttlInterval    time.Duration
	minTTL         time.Duration
	maxTTL         time.Duration
	leasesLock     sync.RWMutex
	startOnce      sync.Once
	stopOnce       sync.Once
}

func NewLeaseManager(ttlInterval time.Duration) *LeaseManager {
	return &LeaseManager{
		leases:       make(map[string]*LeaseEntry),
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
		if now.After(lease.Expires) {
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

func (lm *LeaseManager) UpdateLease(lease *Lease) bool {
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
	if lm.namePattern != nil && lease.Name != "" && !lm.namePattern.MatchString(lease.Name) {
		return false
	}
	if lm.minTTL > 0 || lm.maxTTL > 0 {
		ttl := time.Until(lease.Expires)
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

	// Parse metadata once for cached access
	var parsedMeta *types.ParsedMetadata
	metadataJSON, _ := json.Marshal(lease.Metadata)
	if len(metadataJSON) > 0 {
		var meta types.ParsedMetadata
		if err := json.Unmarshal(metadataJSON, &meta); err == nil {
			parsedMeta = &meta
		}
	}

	var firstSeen time.Time
	if existing, exists := lm.leases[identityID]; exists {
		firstSeen = existing.FirstSeen
	}
	if firstSeen.IsZero() {
		firstSeen = time.Now()
	}

	lm.leases[identityID] = &LeaseEntry{
		Lease:          lease,
		Expires:        lease.Expires,
		LastSeen:       time.Now(),
		FirstSeen:      firstSeen,
		ParsedMetadata: parsedMeta,
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

func (lm *LeaseManager) GetLeaseByName(name string) (*LeaseEntry, bool) {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()

	if name == "" {
		return nil, false
	}

	now := time.Now()
	for _, lease := range lm.leases {
		if strings.EqualFold(lease.Lease.Name, name) {
			if _, banned := lm.bannedLeases[lease.Lease.ID]; banned {
				continue
			}
			if now.After(lease.Expires) {
				continue
			}
			return lease, true
		}
	}
	return nil, false
}

func (lm *LeaseManager) GetAllLeases() []*Lease {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()

	now := time.Now()
	var validLeases []*Lease

	for _, entry := range lm.leases {
		if now.Before(entry.Expires) {
			validLeases = append(validLeases, entry.Lease)
		}
	}

	return validLeases
}

// GetAllLeaseEntries returns all lease entries from the lease manager.
func (lm *LeaseManager) GetAllLeaseEntries() []*LeaseEntry {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()

	now := time.Now()
	var entries []*LeaseEntry

	for _, entry := range lm.leases {
		if now.Before(entry.Expires) {
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

func (lm *LeaseManager) SetTTLBounds(minTTL, maxTTL time.Duration) {
	lm.leasesLock.Lock()
	lm.minTTL = minTTL
	lm.maxTTL = maxTTL
	lm.leasesLock.Unlock()
}
