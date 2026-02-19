package manager

import (
	"net"
	"net/http"
	"strings"
	"sync"
)

// IPManager manages IP-based bans and lease-to-IP mapping.
type IPManager struct {
	mu         sync.RWMutex
	bannedIPs  map[string]struct{} // set of banned IPs
	leaseToIP  map[string]string   // lease ID -> IP address
	ipToLeases map[string][]string // IP -> list of lease IDs (for lookup)

	// pendingIPs stores recent connection IPs in a circular buffer for lease association
	pendingIPsMu    sync.Mutex
	pendingIPsQueue []string
	pendingIPsMax   int // Keep last N IPs
}

// NewIPManager creates a new IP manager.
func NewIPManager() *IPManager {
	return &IPManager{
		bannedIPs:     make(map[string]struct{}),
		leaseToIP:     make(map[string]string),
		ipToLeases:    make(map[string][]string),
		pendingIPsMax: 100,
	}
}

// BanIP adds an IP to the ban list.
func (m *IPManager) BanIP(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bannedIPs[ip] = struct{}{}
}

// UnbanIP removes an IP from the ban list.
func (m *IPManager) UnbanIP(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bannedIPs, ip)
}

// IsIPBanned checks if an IP is banned.
func (m *IPManager) IsIPBanned(ip string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, banned := m.bannedIPs[ip]
	return banned
}

// GetBannedIPs returns all banned IPs.
func (m *IPManager) GetBannedIPs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, 0, len(m.bannedIPs))
	for ip := range m.bannedIPs {
		result = append(result, ip)
	}
	return result
}

// SetBannedIPs sets the banned IPs list (for loading from settings).
func (m *IPManager) SetBannedIPs(ips []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bannedIPs = make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		m.bannedIPs[ip] = struct{}{}
	}
}

// RegisterLeaseIP associates a lease ID with an IP address.
func (m *IPManager) RegisterLeaseIP(leaseID, ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Remove old mapping if exists
	if oldIP, exists := m.leaseToIP[leaseID]; exists && oldIP != ip {
		m.removeLeaseFromIP(leaseID, oldIP)
	}

	m.leaseToIP[leaseID] = ip
	m.ipToLeases[ip] = append(m.ipToLeases[ip], leaseID)
}

// removeLeaseFromIP removes a lease from IP's lease list (must hold lock).
func (m *IPManager) removeLeaseFromIP(leaseID, ip string) {
	leases := m.ipToLeases[ip]
	for i, id := range leases {
		if id == leaseID {
			m.ipToLeases[ip] = append(leases[:i], leases[i+1:]...)
			break
		}
	}
	if len(m.ipToLeases[ip]) == 0 {
		delete(m.ipToLeases, ip)
	}
}

// GetLeaseIP returns the IP address for a lease ID.
func (m *IPManager) GetLeaseIP(leaseID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.leaseToIP[leaseID]
}

// GetIPLeases returns all lease IDs for an IP.
func (m *IPManager) GetIPLeases(ip string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, len(m.ipToLeases[ip]))
	copy(result, m.ipToLeases[ip])
	return result
}

// ExtractClientIP extracts the client IP from an HTTP request.
func ExtractClientIP(r *http.Request) string {
	// Check X-Forwarded-For header first (for proxied requests)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For can contain multiple IPs, take the first one
		if before, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(before)
		}
		return strings.TrimSpace(xff)
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	// Fall back to RemoteAddr
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// StorePendingIP stores a client IP for later association with a lease.
func (m *IPManager) StorePendingIP(ip string) {
	if ip == "" {
		return
	}
	m.pendingIPsMu.Lock()
	defer m.pendingIPsMu.Unlock()
	m.pendingIPsQueue = append(m.pendingIPsQueue, ip)
	if len(m.pendingIPsQueue) > m.pendingIPsMax {
		m.pendingIPsQueue = m.pendingIPsQueue[1:]
	}
}

// PopPendingIP retrieves and removes the oldest pending IP.
func (m *IPManager) PopPendingIP() string {
	m.pendingIPsMu.Lock()
	defer m.pendingIPsMu.Unlock()
	if len(m.pendingIPsQueue) == 0 {
		return ""
	}
	ip := m.pendingIPsQueue[0]
	m.pendingIPsQueue = m.pendingIPsQueue[1:]
	return ip
}
