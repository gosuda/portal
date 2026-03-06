package portal

import (
	"strings"
	"sync"
)

type routeTable struct {
	mu    sync.RWMutex
	exact map[string]string
}

func newRouteTable() *routeTable {
	return &routeTable{exact: make(map[string]string)}
}

func (t *routeTable) Set(host, leaseID string) {
	host = normalizeHostname(host)
	if host == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.exact[host] = leaseID
}

func (t *routeTable) Delete(host string) {
	host = normalizeHostname(host)
	if host == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.exact, host)
}

func (t *routeTable) DeleteLease(hosts []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, host := range hosts {
		delete(t.exact, normalizeHostname(host))
	}
}

func (t *routeTable) Lookup(host string) (string, bool) {
	host = normalizeHostname(host)
	if host == "" {
		return "", false
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	if leaseID, ok := t.exact[host]; ok {
		return leaseID, true
	}

	parts := stringsSplit(host, ".")
	if len(parts) < 3 {
		return "", false
	}
	wildcard := "*." + stringsJoin(parts[1:], ".")
	leaseID, ok := t.exact[wildcard]
	return leaseID, ok
}

func stringsSplit(s, sep string) []string           { return strings.Split(s, sep) }
func stringsJoin(parts []string, sep string) string { return strings.Join(parts, sep) }
