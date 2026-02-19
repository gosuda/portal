package ratelimit

import (
	"io"
	"sync"
	"time"
)

// Bucket is a very simple thread-safe rate limiter.
// It uses a shared timeline (allowAt) with a fixed per-byte duration and
// a maximum slack to model burst capacity.
type Bucket struct {
	mu       sync.Mutex
	perByte  time.Duration // time per byte
	maxSlack time.Duration // maximum credit time (burst)
	allowAt  time.Time     // next allowed time on the timeline
}

// NewBucket creates a limiter for rateBps with burst bytes.
// burst is translated to time slack = burst * perByte.
func NewBucket(rateBps, burst int64) *Bucket {
	if rateBps <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = rateBps
	}
	perByte := time.Second / time.Duration(rateBps)
	if perByte <= 0 {
		perByte = time.Nanosecond
	}
	maxSlack := perByte * time.Duration(burst)
	now := time.Now()
	// Start with full burst credit available
	allowAt := now.Add(-maxSlack)
	return &Bucket{perByte: perByte, maxSlack: maxSlack, allowAt: allowAt}
}

// Take blocks long enough to account for n bytes at the configured rate.
// It is safe for concurrent use and coordinates consumers by a shared timeline.
func (b *Bucket) Take(n int64) {
	if b == nil || n <= 0 {
		return
	}
	b.mu.Lock()
	now := time.Now()
	// Refill slack over time up to maxSlack
	if now.Sub(b.allowAt) > b.maxSlack {
		b.allowAt = now.Add(-b.maxSlack)
	}
	start := b.allowAt
	finish := start.Add(b.perByte * time.Duration(n))
	b.allowAt = finish
	b.mu.Unlock()

	if sleep := finish.Sub(now); sleep > 0 {
		time.Sleep(sleep)
	}
}

// Copy copies from src to dst, enforcing the provided byte-rate bucket if not nil.
// Returns bytes written and any copy error encountered.
func Copy(dst io.Writer, src io.Reader, b *Bucket) (int64, error) {
	if b == nil {
		return io.Copy(dst, src)
	}
	buf := make([]byte, 64*1024)

	var total int64
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			b.Take(int64(nr))
			nw, ew := dst.Write(buf[:nr])
			if nw > 0 {
				total += int64(nw)
			}
			if ew != nil {
				return total, ew
			}
			if nr != nw {
				return total, io.ErrShortWrite
			}
		}
		if er != nil {
			if er == io.EOF {
				break
			}
			return total, er
		}
	}
	return total, nil
}
