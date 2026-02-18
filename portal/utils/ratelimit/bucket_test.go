package ratelimit

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This test keeps expectations deliberately loose to avoid flakiness
// while still catching gross misbehavior.
func TestSimpleRateAndBurst(t *testing.T) {
	rate := int64(1 * 1024 * 1024) // 1 MiB/s
	burst := rate                  // allow ~1s worth of burst
	b := NewBucket(rate, burst)
	require.NotNil(t, b, "bucket should not be nil for positive rate")

	// First half-burst should complete quickly (use a generous threshold)
	start := time.Now()
	b.Take(burst / 2)
	fast := time.Since(start)
	assert.Less(t, fast, 200*time.Millisecond, "half-burst took too long")

	// Taking 2*rate bytes should take roughly ~1s given 1s burst credit.
	start = time.Now()
	b.Take(2 * rate)
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 700*time.Millisecond, "expected at least ~0.7s throttling")
}

// TestNewBucketInvalidRate tests that NewBucket returns nil for non-positive rates.
func TestNewBucketInvalidRate(t *testing.T) {
	tests := []struct {
		name  string
		rate  int64
		burst int64
	}{
		{"zero rate", 0, 100},
		{"negative rate", -100, 100},
		{"negative rate with positive burst", -1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBucket(tt.rate, tt.burst)
			assert.Nil(t, b, "NewBucket(%d, %d) should return nil for invalid rate", tt.rate, tt.burst)
		})
	}
}

// TestNewBucketDefaultBurst tests that burst defaults to rate when burst <= 0.
func TestNewBucketDefaultBurst(t *testing.T) {
	rate := int64(1000)
	b := NewBucket(rate, 0) // zero burst
	require.NotNil(t, b, "NewBucket should not return nil for positive rate with zero burst")
	assert.Positive(t, b.maxSlack, "expected positive maxSlack")

	// burst should default to rate, so maxSlack should equal perByte * rate
	expectedSlack := b.perByte * time.Duration(rate)
	assert.Equal(t, expectedSlack, b.maxSlack)

	// Test with negative burst
	b2 := NewBucket(rate, -100)
	require.NotNil(t, b2, "NewBucket should not return nil for positive rate with negative burst")
	assert.Equal(t, expectedSlack, b2.maxSlack)
}

// TestNewBucketHighRate tests edge case where perByte could be 0 for very high rates.
func TestNewBucketHighRate(t *testing.T) {
	// Use a very high rate that could cause perByte to be 0
	rate := int64(1e18) // Extremely high rate
	burst := int64(100)
	b := NewBucket(rate, burst)
	require.NotNil(t, b, "NewBucket should not return nil for very high rate")
	// perByte should be at least 1 nanosecond
	assert.GreaterOrEqual(t, b.perByte, time.Nanosecond, "perByte too small")
}

// TestTakeNilBucket tests that Take handles nil bucket gracefully.
func TestTakeNilBucket(_ *testing.T) {
	// Should not panic
	var b *Bucket
	b.Take(100) // Should just return without panicking
}

// TestTakeNonPositiveBytes tests that Take handles non-positive byte counts.
func TestTakeNonPositiveBytes(t *testing.T) {
	rate := int64(1000)
	b := NewBucket(rate, rate)
	require.NotNil(t, b, "NewBucket failed")

	// Should not block or panic for non-positive values
	assert.NotPanics(t, func() { b.Take(0) })
	assert.NotPanics(t, func() { b.Take(-1) })
	assert.NotPanics(t, func() { b.Take(-100) })
}

// TestTakeSlackRefill tests the slack refill logic over time.
func TestTakeSlackRefill(t *testing.T) {
	rate := int64(1000) // 1000 bytes/sec
	burst := int64(500) // 0.5 sec burst
	b := NewBucket(rate, burst)
	require.NotNil(t, b, "NewBucket failed")

	// Exhaust the burst
	b.Take(burst)
	initialAllowAt := b.allowAt

	// Wait for slack to refill (more than maxSlack duration)
	time.Sleep(time.Duration(2*burst*int64(time.Second)/rate) + 100*time.Millisecond)

	// Force state update since refill is lazy
	b.Take(1)

	b.mu.Lock()
	allowAtAfterSleep := b.allowAt
	b.mu.Unlock()

	// allowAt should have moved forward due to slack refill cap
	// After sleeping more than maxSlack, the timeline should be capped at now - maxSlack
	assert.NotEqual(t, initialAllowAt, allowAtAfterSleep, "allowAt should have moved forward after sleep")
}

// TestTakeConcurrent tests concurrent Take calls.
func TestTakeConcurrent(t *testing.T) {
	rate := int64(100 * 1024) // 100 KiB/s
	burst := rate / 10
	b := NewBucket(rate, burst)
	require.NotNil(t, b, "NewBucket failed")

	const numGoroutines = 10
	const bytesPerGoroutine = int64(10 * 1024) // 10 KiB each

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	start := time.Now()
	for range numGoroutines {
		go func() {
			defer wg.Done()
			b.Take(bytesPerGoroutine)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Total bytes: numGoroutines * bytesPerGoroutine = 100 KiB
	// At 100 KiB/s, should take roughly 1 second (minus burst)
	// Should at least take some time (not complete instantly)
	assert.GreaterOrEqual(t, elapsed, 500*time.Millisecond, "concurrent Takes completed too quickly")
}

// TestTakeSequentialBurst tests sequential Takes within burst capacity.
func TestTakeSequentialBurst(t *testing.T) {
	rate := int64(10 * 1024) // 10 KiB/s
	burst := rate            // 1 second burst
	b := NewBucket(rate, burst)
	require.NotNil(t, b, "NewBucket failed")

	// All Takes within burst should complete quickly
	start := time.Now()
	for range 10 {
		b.Take(rate / 10) // Take 1/10 of burst each time
	}
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 100*time.Millisecond, "burst Takes took too long")
}

// TestCopyNilBucket tests that Copy with nil bucket just calls io.Copy.
func TestCopyNilBucket(t *testing.T) {
	src := strings.NewReader("hello, world")
	var dst bytes.Buffer

	n, err := Copy(&dst, src, nil)
	require.NoError(t, err, "Copy failed")
	assert.Equal(t, int64(len("hello, world")), n)
	assert.Equal(t, "hello, world", dst.String())
}

// TestCopyWithRateLimit tests that Copy properly rate limits.
func TestCopyWithRateLimit(t *testing.T) {
	rate := int64(512 * 1024) // 512 KiB/s
	burst := rate
	data := make([]byte, 256*1024) // 256 KiB (half burst)

	src := bytes.NewReader(data)
	var dst bytes.Buffer
	b := NewBucket(rate, burst)
	if b == nil {
		t.Fatal("NewBucket failed")
	}

	start := time.Now()
	n, err := Copy(&dst, src, b)
	elapsed := time.Since(start)

	require.NoError(t, err, "Copy failed")
	assert.Equal(t, int64(len(data)), n)
	// Should complete quickly since we're within burst capacity
	assert.Less(t, elapsed, 200*time.Millisecond, "Copy took too long")
}

// TestCopyErrorHandling tests Copy error handling paths.
func TestCopyErrorHandling(t *testing.T) {
	// Test reader error
	errReader := &errReader{err: errors.New("read error")}
	var dst bytes.Buffer
	b := NewBucket(1000, 1000)

	_, err := Copy(&dst, errReader, b)
	require.Error(t, err, "expected error from reader")
	assert.ErrorIs(t, err, errReader.err)
}

// TestCopyShortWrite tests short write detection.
func TestCopyShortWrite(t *testing.T) {
	data := []byte("hello world")
	shortWriter := &shortWriter{maxWrite: 3} // Only writes 3 bytes at a time
	src := bytes.NewReader(data)
	b := NewBucket(1000, 1000)

	n, err := Copy(shortWriter, src, b)
	assert.ErrorIs(t, err, io.ErrShortWrite)
	// Should have written some bytes but not all
	assert.Greater(t, n, int64(0), "expected some bytes to be written")
}

// TestCopyConcurrent tests concurrent Copy operations.
func TestCopyConcurrent(t *testing.T) {
	rate := int64(100 * 1024) // 100 KiB/s
	burst := rate / 10
	b := NewBucket(rate, burst)
	if b == nil {
		t.Fatal("NewBucket failed")
	}

	const numGoroutines = 5
	data := make([]byte, 10*1024) // 10 KiB each

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	start := time.Now()
	for range numGoroutines {
		go func() {
			defer wg.Done()
			src := bytes.NewReader(data)
			var dst bytes.Buffer
			Copy(&dst, src, b)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// Should take some time due to rate limiting
	if elapsed < 300*time.Millisecond {
		t.Errorf("concurrent Copies completed too quickly: %v", elapsed)
	}
}

// TestCopyWriteError tests write error handling in Copy.
func TestCopyWriteError(t *testing.T) {
	data := []byte("test data")
	src := bytes.NewReader(data)
	errWriter := &errWriter{err: errors.New("write error")}
	b := NewBucket(1000, 1000)

	n, err := Copy(errWriter, src, b)
	require.Error(t, err, "expected write error")
	assert.ErrorIs(t, err, errWriter.err)
	assert.Greater(t, n, int64(0), "expected some bytes to be written before error")
}

// TestTakeLargeBytes tests Take with very large byte counts.
func TestTakeLargeBytes(t *testing.T) {
	rate := int64(1024) // 1 KiB/s
	burst := rate       // 1 second burst
	b := NewBucket(rate, burst)
	require.NotNil(t, b, "NewBucket failed")

	// Take a large amount that exceeds burst
	start := time.Now()
	b.Take(rate * 5) // 5 seconds worth
	elapsed := time.Since(start)

	// Should take several seconds (minus burst)
	assert.GreaterOrEqual(t, elapsed, 3*time.Second, "large Take completed too quickly")
}

// TestBufferPool tests that the buffer pool works correctly.
func TestBufferPool(t *testing.T) {
	data := make([]byte, 64*1024) // Exactly buffer size
	src := bytes.NewReader(data)
	var dst bytes.Buffer
	b := NewBucket(100*1024*1024, 100*1024*1024)

	n, err := Copy(&dst, src, b)
	require.NoError(t, err, "Copy failed")
	assert.Equal(t, int64(len(data)), n)

	// Test with data larger than buffer
	largeData := make([]byte, 200*1024) // 200 KiB (larger than 64 KiB buffer)
	src2 := bytes.NewReader(largeData)
	var dst2 bytes.Buffer

	n2, err := Copy(&dst2, src2, b)
	require.NoError(t, err, "Copy failed")
	assert.Equal(t, int64(len(largeData)), n2)
}

// Helper types for testing

type errReader struct {
	err error
}

func (r *errReader) Read(_ []byte) (n int, err error) {
	return 0, r.err
}

type shortWriter struct {
	maxWrite int
	written  int
}

func (w *shortWriter) Write(p []byte) (n int, err error) {
	if w.maxWrite <= 0 {
		return 0, io.ErrShortWrite
	}
	if len(p) > w.maxWrite {
		n = w.maxWrite
		w.maxWrite = 0
		return n, io.ErrShortWrite
	}
	n = len(p)
	w.written += n
	return n, nil
}

type errWriter struct {
	err error
}

func (w *errWriter) Write(p []byte) (n int, err error) {
	return len(p), w.err
}
