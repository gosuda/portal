package main

import (
	"io"
	"sync"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"
	"gosuda.org/portal/cmd/relay-server/ratelimit"
)

// BPSManager manages per-lease bytes-per-second rate limiting
type BPSManager struct {
	mu         sync.Mutex
	bpsLimits  map[string]int64             // leaseID -> bytes-per-second (0 = unlimited)
	bpsBuckets map[string]*ratelimit.Bucket // leaseID -> rate limit bucket
	defaultBPS int64                        // default bytes-per-second for new leases
}

// NewBPSManager creates a new BPS manager
func NewBPSManager() *BPSManager {
	return &BPSManager{
		bpsLimits:  make(map[string]int64),
		bpsBuckets: make(map[string]*ratelimit.Bucket),
		defaultBPS: 0,
	}
}

// SetBPSLimit sets the BPS limit for a lease
func (m *BPSManager) SetBPSLimit(leaseID string, bps int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if bps <= 0 {
		delete(m.bpsLimits, leaseID)
		delete(m.bpsBuckets, leaseID)
		return
	}
	m.bpsLimits[leaseID] = bps
	// Reset bucket to apply new rate
	delete(m.bpsBuckets, leaseID)
}

// GetBPSLimit returns the BPS limit for a lease (0 = unlimited)
func (m *BPSManager) GetBPSLimit(leaseID string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.bpsLimits[leaseID]; ok {
		return v
	}
	return 0
}

// GetAllBPSLimits returns a copy of all BPS limits
func (m *BPSManager) GetAllBPSLimits() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]int64, len(m.bpsLimits))
	for k, v := range m.bpsLimits {
		result[k] = v
	}
	return result
}

// SetDefaultBPS sets the default BPS limit for new leases
func (m *BPSManager) SetDefaultBPS(bps int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if bps < 0 {
		bps = 0
	}
	m.defaultBPS = bps
}

// GetDefaultBPS returns the default BPS limit
func (m *BPSManager) GetDefaultBPS() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defaultBPS
}

// GetBucket returns a rate limit bucket for a lease, creating one if needed
func (m *BPSManager) GetBucket(leaseID string) *ratelimit.Bucket {
	m.mu.Lock()
	defer m.mu.Unlock()

	bps, ok := m.bpsLimits[leaseID]
	if !ok || bps <= 0 {
		return nil // No limit
	}

	if bucket, exists := m.bpsBuckets[leaseID]; exists {
		return bucket
	}

	// Create new bucket
	bucket := ratelimit.NewBucket(bps, bps)
	m.bpsBuckets[leaseID] = bucket
	log.Debug().
		Str("lease_id", leaseID).
		Int64("bps", bps).
		Msg("[BPS] Created rate limit bucket")
	return bucket
}

// CleanupLease removes BPS data for a lease
func (m *BPSManager) CleanupLease(leaseID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bpsLimits, leaseID)
	delete(m.bpsBuckets, leaseID)
}

// Copy copies data with rate limiting
func (m *BPSManager) Copy(dst io.Writer, src io.Reader, leaseID string) (int64, error) {
	bucket := m.GetBucket(leaseID)
	return ratelimit.Copy(dst, src, bucket)
}

// Connection tracking for relay (package level)
var (
	relayedPerLeaseCount = make(map[string]int)
	relayLimitsLock      sync.Mutex
)

// establishRelayWithBPS sets up bidirectional relay with BPS limiting
func establishRelayWithBPS(clientStream, leaseStream *yamux.Stream, leaseID string, bpsManager *BPSManager) {
	// Register connection
	relayLimitsLock.Lock()
	relayedPerLeaseCount[leaseID]++
	connectionCount := relayedPerLeaseCount[leaseID]
	relayLimitsLock.Unlock()

	// Log relay start
	bpsLimit := bpsManager.GetBPSLimit(leaseID)
	log.Info().
		Str("lease_id", leaseID).
		Int64("bps_limit", bpsLimit).
		Int("active_connections", connectionCount).
		Msg("[Relay] Starting relay connection")

	// Cleanup function
	defer func() {
		relayLimitsLock.Lock()
		if relayedPerLeaseCount[leaseID] > 0 {
			relayedPerLeaseCount[leaseID]--
		}
		remainingCount := relayedPerLeaseCount[leaseID]
		relayLimitsLock.Unlock()

		log.Info().
			Str("lease_id", leaseID).
			Int("remaining_connections", remainingCount).
			Msg("[Relay] Relay connection closed")
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	// Client -> Lease
	go func() {
		defer wg.Done()
		bpsManager.Copy(leaseStream, clientStream, leaseID)
		leaseStream.Close()
	}()

	// Lease -> Client
	go func() {
		defer wg.Done()
		bpsManager.Copy(clientStream, leaseStream, leaseID)
		clientStream.Close()
	}()

	wg.Wait()
}
