package ratelimit

import (
	"io"
	"sync"
	"time"
)

// Bucket is a simple, precise byte-rate limiter (thread-safe).
// All fields are integer-based (bytes, ns) to avoid float overhead.
type Bucket struct {
	mu       sync.Mutex
	rateBps  int64     // bytes per second
	capacity int64     // max tokens (burst), typically = rateBps
	tokens   int64     // current tokens in bytes
	last     time.Time // last refill time
}

// NewBucket creates a bucket with the given rate and burst (bytes).
// If burst <= 0, it defaults to rateBps.
func NewBucket(rateBps int64, burst int64) *Bucket {
	if burst <= 0 {
		burst = rateBps
	}
	return &Bucket{rateBps: rateBps, capacity: burst, tokens: burst, last: time.Now()}
}

// Take blocks until n bytes worth of tokens are available, then consumes them.
func (b *Bucket) Take(n int64) {
	for {
		var sleep time.Duration
		b.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(b.last)
		if elapsed > 0 {
			refill := (elapsed.Nanoseconds() * b.rateBps) / int64(time.Second)
			if refill > 0 {
				b.tokens += refill
				if b.tokens > b.capacity {
					b.tokens = b.capacity
				}
				b.last = now
			}
		}
		if b.tokens >= n {
			b.tokens -= n
			b.mu.Unlock()
			return
		}
		deficit := n - b.tokens
		nsNeeded := max((deficit*int64(time.Second))/b.rateBps, int64(time.Millisecond))
		sleep = time.Duration(nsNeeded)
		b.mu.Unlock()
		time.Sleep(sleep)
	}
}

// internal buffer pool for Copy
var bufPool = sync.Pool{New: func() any { return make([]byte, 64*1024) }}

// Copy copies from src to dst, enforcing the provided byte-rate bucket if not nil.
// Returns bytes written and any copy error encountered.
func Copy(dst io.Writer, src io.Reader, b *Bucket) (int64, error) {
	if b == nil {
		return io.Copy(dst, src)
	}
	buf := bufPool.Get().([]byte)
	defer bufPool.Put(buf)

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
