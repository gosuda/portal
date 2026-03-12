package portal

import (
	"errors"
	"sync"
)

var errPortExhausted = errors.New("no udp ports available")

// portAllocator manages a pool of UDP ports for dynamic per-lease allocation.
type portAllocator struct {
	available []int
	inUse     map[int]struct{}
	mu        sync.Mutex
}

func newPortAllocator(min, max int) *portAllocator {
	available := make([]int, 0, max-min+1)
	for p := min; p <= max; p++ {
		available = append(available, p)
	}
	return &portAllocator{
		available: available,
		inUse:     make(map[int]struct{}),
	}
}

// Allocate returns the next available port from the pool.
func (a *portAllocator) Allocate() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.available) == 0 {
		return 0, errPortExhausted
	}
	port := a.available[0]
	a.available = a.available[1:]
	a.inUse[port] = struct{}{}
	return port, nil
}

// Release returns a port back to the available pool.
func (a *portAllocator) Release(port int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if _, ok := a.inUse[port]; !ok {
		return
	}
	delete(a.inUse, port)
	a.available = append(a.available, port)
}
