package portal

import (
	"errors"
	"sort"
	"sync"
	"time"
)

var errPortExhausted = errors.New("no udp ports available")

// portReservation holds a released port for a grace period so the same lease
// name can reclaim it on rapid reconnect.
type portReservation struct {
	port      int
	expiresAt time.Time
}

// portAllocator manages a pool of UDP ports for dynamic per-lease allocation.
//
// Features:
//   - Sticky allocation: re-registering the same lease name within the grace
//     period returns the previously assigned port.
//   - Grace period: released ports are held in a reservation map for the
//     configured duration before returning to the free pool.
//   - Sorted reuse: free ports are kept in ascending order so the lowest
//     available port is always allocated first.
type portAllocator struct {
	available []int                      // sorted ascending
	inUse     map[int]string             // port → lease name
	reserved  map[string]portReservation // lease name → reservation
	grace     time.Duration
	mu        sync.Mutex
}

func newPortAllocator(min, max int, grace time.Duration) *portAllocator {
	available := make([]int, 0, max-min+1)
	for p := min; p <= max; p++ {
		available = append(available, p)
	}
	return &portAllocator{
		available: available,
		inUse:     make(map[int]string),
		reserved:  make(map[string]portReservation),
		grace:     grace,
	}
}

// Allocate returns a UDP port for the given lease name.
// If the name has a non-expired reservation the same port is returned.
// Otherwise the lowest available port is allocated.
func (a *portAllocator) Allocate(name string) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.cleanupExpiredLocked(time.Now())

	// Reclaim reserved port for same name.
	if res, ok := a.reserved[name]; ok {
		delete(a.reserved, name)
		a.inUse[res.port] = name
		return res.port, nil
	}

	if len(a.available) == 0 {
		return 0, errPortExhausted
	}

	port := a.available[0]
	a.available = a.available[1:]
	a.inUse[port] = name
	return port, nil
}

// Release moves a port from in-use to reserved state. The port is held for
// the grace period so the same lease name can reclaim it.
func (a *portAllocator) Release(port int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	name, ok := a.inUse[port]
	if !ok {
		return
	}
	delete(a.inUse, port)

	// If the name already has a different reservation (shouldn't happen in
	// normal flow), return that old port to the free pool first.
	if prev, exists := a.reserved[name]; exists {
		a.sortedInsertLocked(prev.port)
	}

	a.reserved[name] = portReservation{
		port:      port,
		expiresAt: time.Now().Add(a.grace),
	}

	a.cleanupExpiredLocked(time.Now())
}

// cleanupExpiredLocked moves expired reservations back to the sorted available
// pool. Caller must hold a.mu.
func (a *portAllocator) cleanupExpiredLocked(now time.Time) {
	for name, res := range a.reserved {
		if now.After(res.expiresAt) {
			delete(a.reserved, name)
			a.sortedInsertLocked(res.port)
		}
	}
}

// sortedInsertLocked inserts port into a.available maintaining ascending order.
// Caller must hold a.mu.
func (a *portAllocator) sortedInsertLocked(port int) {
	i := sort.SearchInts(a.available, port)
	a.available = append(a.available, 0)
	copy(a.available[i+1:], a.available[i:])
	a.available[i] = port
}
