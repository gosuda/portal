package ratelimit

import (
	"testing"
	"time"
)

// This test keeps expectations deliberately loose to avoid flakiness
// while still catching gross misbehavior.
func TestSimpleRateAndBurst(t *testing.T) {
	rate := int64(1 * 1024 * 1024) // 1 MiB/s
	burst := rate                  // allow ~1s worth of burst
	b := NewBucket(rate, burst)
	if b == nil {
		t.Fatalf("bucket should not be nil for positive rate")
	}

	// First half-burst should complete quickly (use a generous threshold)
	start := time.Now()
	b.Take(burst / 2)
	fast := time.Since(start)
	if fast > 200*time.Millisecond {
		t.Fatalf("half-burst took too long: %v", fast)
	}

	// Taking 2*rate bytes should take roughly ~1s given 1s burst credit.
	start = time.Now()
	b.Take(2 * rate)
	elapsed := time.Since(start)
	if elapsed < 700*time.Millisecond { // be tolerant to scheduling variance
		t.Fatalf("expected at least ~0.7s throttling, got %v", elapsed)
	}
}
