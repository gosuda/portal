package portal

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ParsedMetadata holds struct-parsed metadata for better access
type ParsedMetadata struct {
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Thumbnail   string   `json:"thumbnail"`
	Owner       string   `json:"owner"`
	Hide        bool     `json:"hide"`
}

// Lease represents a registered service.
type Lease struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Metadata     Metadata  `json:"metadata"`
	Expires      time.Time `json:"expires"`
	TLSMode      string    `json:"tls_mode"` // no-tls, self, keyless
	ReverseToken string    `json:"-"`        // shared secret for reverse connect authentication
}

// Metadata holds service metadata
type Metadata struct {
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Thumbnail   string   `json:"thumbnail,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	Hide        bool     `json:"hide,omitempty"`
}

// LeaseEntry represents a registered lease with expiration tracking.
type LeaseEntry struct {
	Lease          *Lease
	Expires        time.Time
	LastSeen       time.Time
	FirstSeen      time.Time
	ParsedMetadata *ParsedMetadata // Cached parsed metadata
}

type LeaseManager struct {
	leases      map[string]*LeaseEntry // Key: lease ID
	leasesLock  sync.RWMutex
	stopCh      chan struct{}
	ttlInterval time.Duration

	// policy controls
	bannedLeases   map[string]struct{}
	namePattern    *regexp.Regexp
	minTTL         time.Duration // 0 = no bound
	maxTTL         time.Duration // 0 = no bound
	onLeaseDeleted func(string)
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
	var parsedMeta *ParsedMetadata
	metadataJSON, _ := json.Marshal(lease.Metadata)
	if len(metadataJSON) > 0 {
		var meta ParsedMetadata
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

// GetAllLeaseEntries returns all lease entries from the lease manager
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

func (lm *LeaseManager) SetTTLBounds(min, max time.Duration) {
	lm.leasesLock.Lock()
	lm.minTTL = min
	lm.maxTTL = max
	lm.leasesLock.Unlock()
}
