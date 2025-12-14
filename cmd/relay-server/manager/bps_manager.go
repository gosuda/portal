package manager

import (
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"
)

// BPSManager manages per-lease bytes-per-second rate limiting
type BPSManager struct {
	mu         sync.Mutex
	bpsLimits  map[string]int64   // leaseID -> bytes-per-second (0 = unlimited)
	bpsBuckets map[string]*Bucket // leaseID -> rate limit bucket
	defaultBPS int64              // default bytes-per-second for new leases
}

// NewBPSManager creates a new BPS manager
func NewBPSManager() *BPSManager {
	return &BPSManager{
		bpsLimits:  make(map[string]int64),
		bpsBuckets: make(map[string]*Bucket),
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
func (m *BPSManager) GetBucket(leaseID string) *Bucket {
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
	bucket := NewBucket(bps, bps)
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
	return Copy(dst, src, bucket)
}

// EstablishRelayWithBPS sets up bidirectional relay with BPS limiting.
// Connection tracking is handled by RelayServer's event loop (cmdCheckAndIncLimit/cmdDecLimit).
func EstablishRelayWithBPS(clientStream, leaseStream *yamux.Stream, leaseID string, bpsManager *BPSManager) {
	bpsLimit := bpsManager.GetBPSLimit(leaseID)
	log.Info().
		Str("lease_id", leaseID).
		Int64("bps_limit", bpsLimit).
		Msg("[Relay] Starting relay connection")

	defer func() {
		log.Info().
			Str("lease_id", leaseID).
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

// Bucket is a thread-safe rate limiter that supports multiple concurrent connections
// sharing the same bandwidth limit. It uses a token bucket algorithm where tokens
// represent bytes, and the bucket refills at the configured rate.
type Bucket struct {
	mu sync.Mutex

	rateBps    int64   // bytes per second limit
	tokens     float64 // current available tokens (bytes)
	maxTokens  float64 // maximum tokens (burst size)
	lastRefill time.Time

	// Stats
	totalBytes   int64
	totalWaited  int64 // total wait time in nanoseconds
	throttleHits int64 // number of times we had to wait
}

// NewBucket creates a limiter for rateBps with burst bytes.
// Multiple connections can share this bucket for fair bandwidth distribution.
func NewBucket(rateBps int64, burst int64) *Bucket {
	if rateBps <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = rateBps // default burst = 1 second worth
	}
	return &Bucket{
		rateBps:    rateBps,
		tokens:     float64(burst), // start with full burst
		maxTokens:  float64(burst),
		lastRefill: time.Now(),
	}
}

// Take requests n bytes from the bucket. If not enough tokens are available,
// it waits until sufficient tokens accumulate. This ensures fair distribution
// among multiple concurrent connections sharing the same bucket.
func (b *Bucket) Take(n int64) {
	if b == nil || n <= 0 {
		return
	}

	needed := float64(n)

	for {
		b.mu.Lock()

		// Refill tokens based on elapsed time
		now := time.Now()
		elapsed := now.Sub(b.lastRefill).Seconds()
		b.tokens += elapsed * float64(b.rateBps)
		if b.tokens > b.maxTokens {
			b.tokens = b.maxTokens
		}
		b.lastRefill = now

		if b.tokens >= needed {
			// Enough tokens available, consume them
			b.tokens -= needed
			b.mu.Unlock()
			atomic.AddInt64(&b.totalBytes, n)
			return
		}

		// Calculate how long to wait for enough tokens
		deficit := needed - b.tokens
		waitTime := time.Duration(deficit / float64(b.rateBps) * float64(time.Second))

		// Take whatever tokens are available now
		if b.tokens > 0 {
			needed -= b.tokens
			b.tokens = 0
		}

		b.mu.Unlock()

		// Wait for tokens to accumulate
		if waitTime > 0 {
			atomic.AddInt64(&b.throttleHits, 1)
			atomic.AddInt64(&b.totalWaited, int64(waitTime))
			log.Debug().
				Int64("bytes_requested", n).
				Int64("rate_bps", b.rateBps).
				Dur("wait_time", waitTime).
				Msg("[RateLimit] Throttling - waiting for bandwidth")
			time.Sleep(waitTime)
		}
	}
}

// TakeWithTimeout requests n bytes but returns false if it would take longer
// than maxWait to acquire them. Returns true if tokens were acquired.
func (b *Bucket) TakeWithTimeout(n int64, maxWait time.Duration) bool {
	if b == nil || n <= 0 {
		return true
	}

	deadline := time.Now().Add(maxWait)
	needed := float64(n)

	for {
		b.mu.Lock()

		// Refill tokens
		now := time.Now()
		elapsed := now.Sub(b.lastRefill).Seconds()
		b.tokens += elapsed * float64(b.rateBps)
		if b.tokens > b.maxTokens {
			b.tokens = b.maxTokens
		}
		b.lastRefill = now

		if b.tokens >= needed {
			b.tokens -= needed
			b.mu.Unlock()
			atomic.AddInt64(&b.totalBytes, n)
			return true
		}

		// Check if we have time to wait
		deficit := needed - b.tokens
		waitTime := time.Duration(deficit / float64(b.rateBps) * float64(time.Second))

		if now.Add(waitTime).After(deadline) {
			b.mu.Unlock()
			return false // Would take too long
		}

		if b.tokens > 0 {
			needed -= b.tokens
			b.tokens = 0
		}

		b.mu.Unlock()

		if waitTime > 0 {
			atomic.AddInt64(&b.throttleHits, 1)
			atomic.AddInt64(&b.totalWaited, int64(waitTime))
			time.Sleep(waitTime)
		}
	}
}

// Available returns the current number of available tokens (bytes)
func (b *Bucket) Available() float64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	// Refill first
	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * float64(b.rateBps)
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now

	return b.tokens
}

// Rate returns the configured rate in bytes per second
func (b *Bucket) Rate() int64 {
	if b == nil {
		return 0
	}
	return b.rateBps
}

// Stats returns current statistics
func (b *Bucket) Stats() (totalBytes, throttleHits int64, totalWaited time.Duration) {
	return atomic.LoadInt64(&b.totalBytes),
		atomic.LoadInt64(&b.throttleHits),
		time.Duration(atomic.LoadInt64(&b.totalWaited))
}

// internal buffer pool for Copy - 64KB reduces Take() call frequency and lock contention
var bufPool = sync.Pool{New: func() any { return make([]byte, 64*1024) }}

// Copy copies from src to dst, enforcing the provided byte-rate bucket if not nil.
// Multiple Copy calls sharing the same bucket will fairly share the bandwidth.
// Returns bytes written and any copy error encountered.
func Copy(dst io.Writer, src io.Reader, b *Bucket) (int64, error) {
	if b == nil {
		return io.Copy(dst, src)
	}
	buf := bufPool.Get().([]byte)
	defer bufPool.Put(buf)

	var total int64
	startTime := time.Now()

	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			// Take rate limit BEFORE writing - this delays the write if needed
			// Multiple connections sharing this bucket will wait fairly
			b.Take(int64(nr))
			nw, ew := dst.Write(buf[:nr])
			if nw > 0 {
				total += int64(nw)
			}
			if ew != nil {
				logCopyStats(b, total, startTime)
				return total, ew
			}
			if nr != nw {
				logCopyStats(b, total, startTime)
				return total, io.ErrShortWrite
			}
		}
		if er != nil {
			if er == io.EOF {
				break
			}
			logCopyStats(b, total, startTime)
			return total, er
		}
	}
	logCopyStats(b, total, startTime)
	return total, nil
}

// logCopyStats logs summary statistics when copy completes
func logCopyStats(b *Bucket, totalBytes int64, startTime time.Time) {
	if b == nil || totalBytes == 0 {
		return
	}
	elapsed := time.Since(startTime)
	if elapsed > 0 {
		actualBps := float64(totalBytes) / elapsed.Seconds()
		throttleHits := atomic.LoadInt64(&b.throttleHits)
		totalWaited := time.Duration(atomic.LoadInt64(&b.totalWaited))
		log.Debug().
			Int64("total_bytes", totalBytes).
			Int64("rate_limit_bps", b.rateBps).
			Float64("actual_bps", actualBps).
			Dur("elapsed", elapsed).
			Int64("throttle_hits", throttleHits).
			Dur("total_waited", totalWaited).
			Msg("[RateLimit] Copy completed")
	}
}
