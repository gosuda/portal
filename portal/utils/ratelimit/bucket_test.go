package ratelimit

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
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

// TestNewBucketInvalidRate tests that NewBucket returns nil for non-positive rates
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
			if b != nil {
				t.Errorf("NewBucket(%d, %d) should return nil for invalid rate", tt.rate, tt.burst)
			}
		})
	}
}

// TestNewBucketDefaultBurst tests that burst defaults to rate when burst <= 0
func TestNewBucketDefaultBurst(t *testing.T) {
	rate := int64(1000)
	b := NewBucket(rate, 0) // zero burst
	if b == nil {
		t.Fatal("NewBucket should not return nil for positive rate with zero burst")
	}
	if b.maxSlack <= 0 {
		t.Errorf("expected positive maxSlack, got %v", b.maxSlack)
	}

	// burst should default to rate, so maxSlack should equal perByte * rate
	expectedSlack := b.perByte * time.Duration(rate)
	if b.maxSlack != expectedSlack {
		t.Errorf("maxSlack = %v, want %v", b.maxSlack, expectedSlack)
	}

	// Test with negative burst
	b2 := NewBucket(rate, -100)
	if b2 == nil {
		t.Fatal("NewBucket should not return nil for positive rate with negative burst")
	}
	if b2.maxSlack != expectedSlack {
		t.Errorf("maxSlack with negative burst = %v, want %v", b2.maxSlack, expectedSlack)
	}
}

// TestNewBucketHighRate tests edge case where perByte could be 0 for very high rates
func TestNewBucketHighRate(t *testing.T) {
	// Use a very high rate that could cause perByte to be 0
	rate := int64(1e18) // Extremely high rate
	burst := int64(100)
	b := NewBucket(rate, burst)
	if b == nil {
		t.Fatal("NewBucket should not return nil for very high rate")
	}
	// perByte should be at least 1 nanosecond
	if b.perByte < time.Nanosecond {
		t.Errorf("perByte = %v, want >= %v", b.perByte, time.Nanosecond)
	}
}

// TestTakeNilBucket tests that Take handles nil bucket gracefully
func TestTakeNilBucket(t *testing.T) {
	// Should not panic
	var b *Bucket = nil
	b.Take(100) // Should just return without panicking
}

// TestTakeNonPositiveBytes tests that Take handles non-positive byte counts
func TestTakeNonPositiveBytes(t *testing.T) {
	rate := int64(1000)
	b := NewBucket(rate, rate)
	if b == nil {
		t.Fatal("NewBucket failed")
	}

	// Should not block or panic for non-positive values
	b.Take(0)
	b.Take(-1)
	b.Take(-100)
}

// TestTakeSlackRefill tests the slack refill logic over time
func TestTakeSlackRefill(t *testing.T) {
	rate := int64(1000) // 1000 bytes/sec
	burst := int64(500) // 0.5 sec burst
	b := NewBucket(rate, burst)
	if b == nil {
		t.Fatal("NewBucket failed")
	}

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
	if allowAtAfterSleep.Equal(initialAllowAt) {
		t.Error("allowAt should have moved forward after sleep")
	}
}

// TestTakeConcurrent tests concurrent Take calls
func TestTakeConcurrent(t *testing.T) {
	rate := int64(100 * 1024) // 100 KiB/s
	burst := rate / 10
	b := NewBucket(rate, burst)
	if b == nil {
		t.Fatal("NewBucket failed")
	}

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
	if elapsed < 500*time.Millisecond {
		t.Errorf("concurrent Takes completed too quickly: %v", elapsed)
	}
}

// TestTakeSequentialBurst tests sequential Takes within burst capacity
func TestTakeSequentialBurst(t *testing.T) {
	rate := int64(10 * 1024) // 10 KiB/s
	burst := rate            // 1 second burst
	b := NewBucket(rate, burst)
	if b == nil {
		t.Fatal("NewBucket failed")
	}

	// All Takes within burst should complete quickly
	start := time.Now()
	for range 10 {
		b.Take(rate / 10) // Take 1/10 of burst each time
	}
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("burst Takes took too long: %v", elapsed)
	}
}

// TestCopyNilBucket tests that Copy with nil bucket just calls io.Copy
func TestCopyNilBucket(t *testing.T) {
	src := strings.NewReader("hello, world")
	var dst bytes.Buffer

	n, err := Copy(&dst, src, nil)
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}
	if n != int64(len("hello, world")) {
		t.Errorf("copied %d bytes, want %d", n, len("hello, world"))
	}
	if dst.String() != "hello, world" {
		t.Errorf("copied data = %q, want %q", dst.String(), "hello, world")
	}
}

// TestCopyWithRateLimit tests that Copy properly rate limits
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

	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}
	if n != int64(len(data)) {
		t.Errorf("copied %d bytes, want %d", n, len(data))
	}
	// Should complete quickly since we're within burst capacity
	if elapsed > 200*time.Millisecond {
		t.Errorf("Copy took too long: %v", elapsed)
	}
}

// TestCopyErrorHandling tests Copy error handling paths
func TestCopyErrorHandling(t *testing.T) {
	// Test reader error
	errReader := &errReader{err: errors.New("read error")}
	var dst bytes.Buffer
	b := NewBucket(1000, 1000)

	_, err := Copy(&dst, errReader, b)
	if err == nil {
		t.Error("expected error from reader, got nil")
	}
	if err != errReader.err {
		t.Errorf("got error %v, want %v", err, errReader.err)
	}
}

// TestCopyShortWrite tests short write detection
func TestCopyShortWrite(t *testing.T) {
	data := []byte("hello world")
	shortWriter := &shortWriter{maxWrite: 3} // Only writes 3 bytes at a time
	src := bytes.NewReader(data)
	b := NewBucket(1000, 1000)

	n, err := Copy(shortWriter, src, b)
	if err != io.ErrShortWrite {
		t.Errorf("got error %v, want %v", err, io.ErrShortWrite)
	}
	// Should have written some bytes but not all
	if n == 0 {
		t.Error("expected some bytes to be written")
	}
}

// TestCopyConcurrent tests concurrent Copy operations
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

// TestCopyWriteError tests write error handling in Copy
func TestCopyWriteError(t *testing.T) {
	data := []byte("test data")
	src := bytes.NewReader(data)
	errWriter := &errWriter{err: errors.New("write error")}
	b := NewBucket(1000, 1000)

	n, err := Copy(errWriter, src, b)
	if err == nil {
		t.Error("expected write error, got nil")
	}
	if err != errWriter.err {
		t.Errorf("got error %v, want %v", err, errWriter.err)
	}
	if n == 0 {
		t.Error("expected some bytes to be written before error")
	}
}

// TestTakeLargeBytes tests Take with very large byte counts
func TestTakeLargeBytes(t *testing.T) {
	rate := int64(1024) // 1 KiB/s
	burst := rate       // 1 second burst
	b := NewBucket(rate, burst)
	if b == nil {
		t.Fatal("NewBucket failed")
	}

	// Take a large amount that exceeds burst
	start := time.Now()
	b.Take(rate * 5) // 5 seconds worth
	elapsed := time.Since(start)

	// Should take several seconds (minus burst)
	if elapsed < 3*time.Second {
		t.Errorf("large Take completed too quickly: %v", elapsed)
	}
}

// TestBufferPool tests that the buffer pool works correctly
func TestBufferPool(t *testing.T) {
	data := make([]byte, 64*1024) // Exactly buffer size
	src := bytes.NewReader(data)
	var dst bytes.Buffer
	b := NewBucket(100*1024*1024, 100*1024*1024)

	n, err := Copy(&dst, src, b)
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}
	if n != int64(len(data)) {
		t.Errorf("copied %d bytes, want %d", n, len(data))
	}

	// Test with data larger than buffer
	largeData := make([]byte, 200*1024) // 200 KiB (larger than 64 KiB buffer)
	src2 := bytes.NewReader(largeData)
	var dst2 bytes.Buffer

	n2, err := Copy(&dst2, src2, b)
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}
	if n2 != int64(len(largeData)) {
		t.Errorf("copied %d bytes, want %d", n2, len(largeData))
	}
}

// Helper types for testing

type errReader struct {
	err error
}

func (r *errReader) Read(p []byte) (n int, err error) {
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
