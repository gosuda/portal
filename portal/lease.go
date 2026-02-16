package portal

import (
	"encoding/json"
	"regexp"
	"sync"
	"time"

	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
)

// ParsedMetadata holds struct-parsed metadata for better access.
type ParsedMetadata struct {
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Thumbnail   string   `json:"thumbnail"`
	Owner       string   `json:"owner"`
	Hide        bool     `json:"hide"`
}

// LeaseEntry represents a registered lease with expiration tracking.
type LeaseEntry struct {
	Lease          *rdverb.Lease
	Expires        time.Time
	LastSeen       time.Time
	FirstSeen      time.Time
	ConnectionID   int64
	ParsedMetadata *ParsedMetadata // Cached parsed metadata
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

	// Parse metadata once for cached access
	var parsedMeta *ParsedMetadata
	if lease.Metadata != "" {
		var meta struct {
			Description string   `json:"description"`
			Tags        []string `json:"tags"`
			Thumbnail   string   `json:"thumbnail"`
			Owner       string   `json:"owner"`
			Hide        bool     `json:"hide"`
		}
		if err := json.Unmarshal([]byte(lease.Metadata), &meta); err == nil {
			parsedMeta = &ParsedMetadata{
				Description: meta.Description,
				Tags:        meta.Tags,
				Thumbnail:   meta.Thumbnail,
				Owner:       meta.Owner,
				Hide:        meta.Hide,
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

	lm.leases[identityID] = &LeaseEntry{
		Lease:          lease,
		Expires:        expires,
		LastSeen:       time.Now(),
		FirstSeen:      firstSeen,
		ConnectionID:   connectionID,
		ParsedMetadata: parsedMeta,
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
		if lease.Lease.Name == name {
			// Check if banned
			if _, banned := lm.bannedLeases[string(lease.Lease.Identity.Id)]; banned {
				continue
			}
			// Check if expired
			if now.After(lease.Expires) {
				continue
			}
			return lease, true
		}
	}
	return nil, false
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

// Lease policy configuration helpers.
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
			cleanedLeaseIDs = append(cleanedLeaseIDs, leaseID)
		}
	}

	return cleanedLeaseIDs
}
