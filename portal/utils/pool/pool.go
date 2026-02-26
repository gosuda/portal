// Package pool provides shared buffer pools for I/O operations.
package pool

import "sync"

// Buffer64K provides reusable 64KB buffers for io.CopyBuffer to reduce
// per-copy allocations and GC pressure under high concurrency.
// Using *[]byte to avoid interface boxing allocation in sync.Pool.
var Buffer64K = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}
