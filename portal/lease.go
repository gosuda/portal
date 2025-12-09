package portal

import (
	"encoding/json"
	"regexp"
	"sync"
	"time"

	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
)

// ParsedMetadata contains pre-parsed lease metadata fields.
// Defined locally to avoid sdk dependency in core package.
type ParsedMetadata struct {
	Description string
	Tags        []string
	Thumbnail   string
	Owner       string
	Hide        bool
}

// LeaseEntry represents a registered lease with expiration tracking.
type LeaseEntry struct {
	Lease          *rdverb.Lease
	Expires        time.Time
	LastSeen       time.Time
	ConnectionID   int64           // Store the connection ID
	ParsedMetadata *ParsedMetadata // Cached parsed metadata
}

// leaseCmd is the command interface for LeaseManager event loop.
type leaseCmd interface{ leaseCmd() }

type cmdUpdateLease struct {
	lease  *rdverb.Lease
	connID int64
	reply  chan<- bool
}

type cmdDeleteLease struct {
	identity *rdsec.Identity
	reply    chan<- bool
}

type cmdGetLease struct {
	identity *rdsec.Identity
	reply    chan<- leaseResult
}

type cmdGetLeaseByID struct {
	leaseID string
	reply   chan<- leaseResult
}

type cmdGetAllLeases struct {
	reply chan<- []*rdverb.Lease
}

type cmdCleanupByConnID struct {
	connID int64
	reply  chan<- []string
}

type cmdBan struct {
	leaseID string
	done    chan<- struct{}
}

type cmdUnban struct {
	leaseID string
	done    chan<- struct{}
}

type cmdGetBanned struct {
	reply chan<- [][]byte
}

type cmdSetPattern struct {
	pattern string
	reply   chan<- error
}

type cmdSetTTL struct {
	min  time.Duration
	max  time.Duration
	done chan<- struct{}
}

type cmdCleanExpired struct {
	done chan<- struct{}
}

type cmdGetAllEntries struct {
	reply chan<- []*LeaseEntry
}

type cmdGetLeaseALPNs struct {
	leaseID string
	reply   chan<- []string
}

// leaseResult carries a LeaseEntry lookup result.
type leaseResult struct {
	entry  *LeaseEntry
	exists bool
}

// Command interface markers
func (cmdUpdateLease) leaseCmd()     {}
func (cmdDeleteLease) leaseCmd()     {}
func (cmdGetLease) leaseCmd()        {}
func (cmdGetLeaseByID) leaseCmd()    {}
func (cmdGetAllLeases) leaseCmd()    {}
func (cmdCleanupByConnID) leaseCmd() {}
func (cmdBan) leaseCmd()             {}
func (cmdUnban) leaseCmd()           {}
func (cmdGetBanned) leaseCmd()       {}
func (cmdSetPattern) leaseCmd()      {}
func (cmdSetTTL) leaseCmd()          {}
func (cmdCleanExpired) leaseCmd()    {}
func (cmdGetAllEntries) leaseCmd()   {}
func (cmdGetLeaseALPNs) leaseCmd()   {}

// LeaseManager manages lease registrations using a single-threaded event loop.
// All state mutations are processed sequentially via the command channel.
type LeaseManager struct {
	leases      map[string]*LeaseEntry // Key: identity ID
	stopCh      chan struct{}
	ttlInterval time.Duration

	// Event loop
	cmdCh chan leaseCmd
	runWg sync.WaitGroup

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
		cmdCh:        make(chan leaseCmd, 256),
		bannedLeases: make(map[string]struct{}),
	}
}

func (lm *LeaseManager) Start() {
	lm.runWg.Add(1)
	go lm.run()
	go lm.ttlWorker()
}

func (lm *LeaseManager) Stop() {
	close(lm.stopCh)
	lm.runWg.Wait()
}

// run is the main event loop that processes all commands sequentially.
func (lm *LeaseManager) run() {
	defer lm.runWg.Done()
	for {
		select {
		case cmd := <-lm.cmdCh:
			lm.handleCmd(cmd)
		case <-lm.stopCh:
			return
		}
	}
}

// handleCmd dispatches commands to their handlers.
func (lm *LeaseManager) handleCmd(cmd leaseCmd) {
	switch c := cmd.(type) {
	case *cmdUpdateLease:
		c.reply <- lm.updateLeaseInternal(c.lease, c.connID)

	case *cmdDeleteLease:
		identityID := string(c.identity.Id)
		if _, exists := lm.leases[identityID]; exists {
			delete(lm.leases, identityID)
			c.reply <- true
		} else {
			c.reply <- false
		}

	case *cmdGetLease:
		identityID := string(c.identity.Id)
		entry, exists := lm.leases[identityID]
		if !exists || time.Now().After(entry.Expires) {
			c.reply <- leaseResult{nil, false}
		} else {
			c.reply <- leaseResult{entry, true}
		}

	case *cmdGetLeaseByID:
		if _, banned := lm.bannedLeases[c.leaseID]; banned {
			c.reply <- leaseResult{nil, false}
			return
		}
		entry, exists := lm.leases[c.leaseID]
		if !exists || time.Now().After(entry.Expires) {
			c.reply <- leaseResult{nil, false}
		} else {
			c.reply <- leaseResult{entry, true}
		}

	case *cmdGetAllLeases:
		now := time.Now()
		var valid []*rdverb.Lease
		for _, entry := range lm.leases {
			if now.Before(entry.Expires) {
				valid = append(valid, entry.Lease)
			}
		}
		c.reply <- valid

	case *cmdCleanupByConnID:
		var cleaned []string
		for leaseID, entry := range lm.leases {
			if entry.ConnectionID == c.connID {
				delete(lm.leases, leaseID)
				cleaned = append(cleaned, leaseID)
			}
		}
		c.reply <- cleaned

	case *cmdBan:
		lm.bannedLeases[c.leaseID] = struct{}{}
		c.done <- struct{}{}

	case *cmdUnban:
		delete(lm.bannedLeases, c.leaseID)
		c.done <- struct{}{}

	case *cmdGetBanned:
		banned := make([][]byte, 0, len(lm.bannedLeases))
		for id := range lm.bannedLeases {
			banned = append(banned, []byte(id))
		}
		c.reply <- banned

	case *cmdSetPattern:
		if c.pattern == "" {
			lm.namePattern = nil
			c.reply <- nil
			return
		}
		re, err := regexp.Compile(c.pattern)
		if err != nil {
			c.reply <- err
			return
		}
		lm.namePattern = re
		c.reply <- nil

	case *cmdSetTTL:
		lm.minTTL = c.min
		lm.maxTTL = c.max
		c.done <- struct{}{}

	case *cmdCleanExpired:
		now := time.Now()
		for id, entry := range lm.leases {
			if now.After(entry.Expires) {
				delete(lm.leases, id)
			}
		}
		c.done <- struct{}{}

	case *cmdGetAllEntries:
		now := time.Now()
		var entries []*LeaseEntry
		for _, entry := range lm.leases {
			if now.Before(entry.Expires) {
				entries = append(entries, entry)
			}
		}
		c.reply <- entries

	case *cmdGetLeaseALPNs:
		entry, exists := lm.leases[c.leaseID]
		if !exists || time.Now().After(entry.Expires) {
			c.reply <- nil
		} else {
			c.reply <- entry.Lease.Alpn
		}
	}
}

// updateLeaseInternal contains the lease update logic, called within the event loop.
func (lm *LeaseManager) updateLeaseInternal(lease *rdverb.Lease, connectionID int64) bool {
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
			if existingID == identityID {
				continue
			}
			if existingEntry.Lease.Name == lease.Name {
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

	lm.leases[identityID] = &LeaseEntry{
		Lease:          lease,
		Expires:        expires,
		LastSeen:       time.Now(),
		ConnectionID:   connectionID,
		ParsedMetadata: parsedMeta,
	}

	return true
}

func (lm *LeaseManager) ttlWorker() {
	ticker := time.NewTicker(lm.ttlInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			done := make(chan struct{}, 1)
			select {
			case lm.cmdCh <- &cmdCleanExpired{done: done}:
				<-done
			case <-lm.stopCh:
				return
			}
		case <-lm.stopCh:
			return
		}
	}
}

func (lm *LeaseManager) UpdateLease(lease *rdverb.Lease, connectionID int64) bool {
	reply := make(chan bool, 1)
	lm.cmdCh <- &cmdUpdateLease{lease: lease, connID: connectionID, reply: reply}
	return <-reply
}

func (lm *LeaseManager) DeleteLease(identity *rdsec.Identity) bool {
	reply := make(chan bool, 1)
	lm.cmdCh <- &cmdDeleteLease{identity: identity, reply: reply}
	return <-reply
}

func (lm *LeaseManager) GetLease(identity *rdsec.Identity) (*LeaseEntry, bool) {
	reply := make(chan leaseResult, 1)
	lm.cmdCh <- &cmdGetLease{identity: identity, reply: reply}
	result := <-reply
	return result.entry, result.exists
}

func (lm *LeaseManager) GetLeaseByID(leaseID string) (*LeaseEntry, bool) {
	reply := make(chan leaseResult, 1)
	lm.cmdCh <- &cmdGetLeaseByID{leaseID: leaseID, reply: reply}
	result := <-reply
	return result.entry, result.exists
}

func (lm *LeaseManager) GetAllLeases() []*rdverb.Lease {
	reply := make(chan []*rdverb.Lease, 1)
	lm.cmdCh <- &cmdGetAllLeases{reply: reply}
	return <-reply
}

// Lease policy configuration helpers
func (lm *LeaseManager) BanLease(leaseID string) {
	done := make(chan struct{}, 1)
	lm.cmdCh <- &cmdBan{leaseID: leaseID, done: done}
	<-done
}

func (lm *LeaseManager) UnbanLease(leaseID string) {
	done := make(chan struct{}, 1)
	lm.cmdCh <- &cmdUnban{leaseID: leaseID, done: done}
	<-done
}

func (lm *LeaseManager) GetBannedLeases() [][]byte {
	reply := make(chan [][]byte, 1)
	lm.cmdCh <- &cmdGetBanned{reply: reply}
	return <-reply
}

func (lm *LeaseManager) SetNamePattern(pattern string) error {
	reply := make(chan error, 1)
	lm.cmdCh <- &cmdSetPattern{pattern: pattern, reply: reply}
	return <-reply
}

// SetReservedPrefixes removed: reserved prefix policy no longer supported

func (lm *LeaseManager) SetTTLBounds(min, max time.Duration) {
	done := make(chan struct{}, 1)
	lm.cmdCh <- &cmdSetTTL{min: min, max: max, done: done}
	<-done
}

func (lm *LeaseManager) CleanupLeasesByConnectionID(connectionID int64) []string {
	reply := make(chan []string, 1)
	lm.cmdCh <- &cmdCleanupByConnID{connID: connectionID, reply: reply}
	return <-reply
}

// GetAllEntries returns all valid (non-expired) lease entries.
// This method is used by RelayServer.GetAllLeaseEntries.
func (lm *LeaseManager) GetAllEntries() []*LeaseEntry {
	reply := make(chan []*LeaseEntry, 1)
	lm.cmdCh <- &cmdGetAllEntries{reply: reply}
	return <-reply
}

// GetLeaseALPNs returns the ALPN identifiers for a given lease ID.
// This method is used by RelayServer.GetLeaseALPNs.
func (lm *LeaseManager) GetLeaseALPNs(leaseID string) []string {
	reply := make(chan []string, 1)
	lm.cmdCh <- &cmdGetLeaseALPNs{leaseID: leaseID, reply: reply}
	return <-reply
}
