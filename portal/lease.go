package portal

import (
	"encoding/json"
	"sync"
	"time"
)

// Lease represents a registered service in the portal.
type Lease struct {
	ID       string
	Name     string
	Metadata string
}

// ParsedMetadata holds struct-parsed metadata for better access
type ParsedMetadata struct {
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Thumbnail   string   `json:"thumbnail"`
	Owner       string   `json:"owner"`
	Hide        bool     `json:"hide"`
}

// LeaseEntry represents a registered lease with expiration tracking.
type LeaseEntry struct {
	Lease          *Lease
	Expires        time.Time
	LastSeen       time.Time
	FirstSeen      time.Time
	ParsedMetadata *ParsedMetadata // Cached parsed metadata
	ReverseToken   string          `json:"-"` // Authentication token for funnel reverse connections — never serialize.
}

type LeaseManager struct {
	leases      map[string]*LeaseEntry // Key: lease ID
	leasesLock  sync.RWMutex
	stopCh      chan struct{}
	ttlInterval time.Duration

	// policy controls
	bannedLeases map[string]struct{}

	// onLeaseDeleted is called when a lease is removed (TTL expiry or explicit deletion).
	// MUST be invoked OUTSIDE the write lock to prevent deadlocks with
	// ReverseHub/SNI Router cleanup.
	onLeaseDeleted func(leaseID string)
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
	// Collect expired IDs under lock
	lm.leasesLock.Lock()
	var expired []string
	now := time.Now()
	callback := lm.onLeaseDeleted
	for id, lease := range lm.leases {
		if now.After(lease.Expires) {
			delete(lm.leases, id)
			expired = append(expired, id)
		}
	}
	lm.leasesLock.Unlock()

	// Invoke callback OUTSIDE lock to prevent deadlocks
	if callback != nil {
		for _, id := range expired {
			callback(id)
		}
	}
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

// GetAllLeaseEntries returns all non-expired lease entries.
func (lm *LeaseManager) GetAllLeaseEntries() []*LeaseEntry {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()

	var entries []*LeaseEntry
	now := time.Now()

	for _, entry := range lm.leases {
		if now.Before(entry.Expires) {
			entries = append(entries, entry)
		}
	}

	return entries
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

func (lm *LeaseManager) GetBannedLeases() []string {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()
	banned := make([]string, 0, len(lm.bannedLeases))
	for id := range lm.bannedLeases {
		banned = append(banned, id)
	}
	return banned
}

// SetOnLeaseDeleted sets the callback invoked when a lease is removed.
// The callback is always invoked outside the LeaseManager write lock.
func (lm *LeaseManager) SetOnLeaseDeleted(fn func(leaseID string)) {
	lm.leasesLock.Lock()
	lm.onLeaseDeleted = fn
	lm.leasesLock.Unlock()
}

// UpdateLeaseSimple registers or renews a funnel lease using simple string IDs.
// Used by the REST API registry.
func (lm *LeaseManager) UpdateLeaseSimple(leaseID, name, reverseToken string, ttl time.Duration, metadata string) bool {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	if _, banned := lm.bannedLeases[leaseID]; banned {
		return false
	}

	// Name conflict check
	if name != "" && name != "(unnamed)" {
		for existingID, existingEntry := range lm.leases {
			if existingID == leaseID {
				continue
			}
			if existingEntry.Lease != nil && existingEntry.Lease.Name == name {
				return false
			}
		}
	}

	var parsedMeta *ParsedMetadata
	if metadata != "" {
		var meta ParsedMetadata
		if err := json.Unmarshal([]byte(metadata), &meta); err == nil {
			parsedMeta = &meta
		}
	}

	expires := time.Now().Add(ttl)
	var firstSeen time.Time
	if existing, exists := lm.leases[leaseID]; exists {
		firstSeen = existing.FirstSeen
	}
	if firstSeen.IsZero() {
		firstSeen = time.Now()
	}

	lm.leases[leaseID] = &LeaseEntry{
		Lease: &Lease{
			ID:       leaseID,
			Name:     name,
			Metadata: metadata,
		},
		Expires:        expires,
		LastSeen:       time.Now(),
		FirstSeen:      firstSeen,
		ParsedMetadata: parsedMeta,
		ReverseToken:   reverseToken,
	}

	return true
}

// RenewLeaseByID extends the TTL of an existing lease by the given duration.
// Returns false if the lease doesn't exist or is banned.
func (lm *LeaseManager) RenewLeaseByID(leaseID string, ttl time.Duration) bool {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	if _, banned := lm.bannedLeases[leaseID]; banned {
		return false
	}

	entry, exists := lm.leases[leaseID]
	if !exists {
		return false
	}

	entry.Expires = time.Now().Add(ttl)
	entry.LastSeen = time.Now()
	return true
}

// DeleteLeaseByID removes a lease by its string ID.
// The onLeaseDeleted callback is invoked outside the lock.
func (lm *LeaseManager) DeleteLeaseByID(leaseID string) bool {
	lm.leasesLock.Lock()
	_, exists := lm.leases[leaseID]
	if exists {
		delete(lm.leases, leaseID)
	}
	callback := lm.onLeaseDeleted
	lm.leasesLock.Unlock()

	if exists && callback != nil {
		callback(leaseID)
	}

	return exists
}
