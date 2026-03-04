package policy

import (
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// IPFilter manages IP-based bans and lease-to-IP mapping.
type IPFilter struct {
	bannedIPs  map[string]struct{}
	leaseToIP  map[string]string
	ipToLeases map[string][]string
	mu         sync.RWMutex
}

var (
	trustedProxyMu    sync.RWMutex
	trustedProxyCIDRs []*net.IPNet
)

const (
	xForwardedForHeader = "X-Forwarded-For"
	xRealIPHeader       = "X-Real-IP"
)

// NewIPFilter creates a new IP filter.
func NewIPFilter() *IPFilter {
	return &IPFilter{
		bannedIPs:  make(map[string]struct{}),
		leaseToIP:  make(map[string]string),
		ipToLeases: make(map[string][]string),
	}
}

// SetTrustedProxyCIDRs configures which remote peers can supply trusted forwarded headers.
func SetTrustedProxyCIDRs(cidrs []*net.IPNet) {
	trustedProxyMu.Lock()
	defer trustedProxyMu.Unlock()

	if len(cidrs) == 0 {
		trustedProxyCIDRs = nil
		return
	}

	trustedProxyCIDRs = append(make([]*net.IPNet, 0, len(cidrs)), cidrs...)
}

// ParseTrustedProxyCIDRs parses a comma-separated CIDR allowlist for trusted proxy peers.
// Empty input returns nil, nil.
func ParseTrustedProxyCIDRs(raw string) ([]*net.IPNet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	cidrs := make([]*net.IPNet, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		candidate := strings.TrimSpace(part)
		if candidate == "" {
			continue
		}

		_, network, err := net.ParseCIDR(candidate)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", candidate, err)
		}

		networkKey := network.String()
		if _, exists := seen[networkKey]; exists {
			continue
		}

		seen[networkKey] = struct{}{}
		cidrs = append(cidrs, network)
	}

	return cidrs, nil
}

// IsTrustedProxyRemoteAddr reports whether a remote peer is in the trusted proxy allowlist.
func IsTrustedProxyRemoteAddr(remoteAddr string) bool {
	remoteIP := parseRemoteAddrIP(remoteAddr)
	if remoteIP == nil {
		return false
	}

	trustedProxyMu.RLock()
	defer trustedProxyMu.RUnlock()

	for _, network := range trustedProxyCIDRs {
		if network != nil && network.Contains(remoteIP) {
			return true
		}
	}

	return false
}

// BanIP adds an IP to the ban list.
func (m *IPFilter) BanIP(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bannedIPs[ip] = struct{}{}
}

// UnbanIP removes an IP from the ban list.
func (m *IPFilter) UnbanIP(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bannedIPs, ip)
}

// IsIPBanned checks if an IP is banned.
func (m *IPFilter) IsIPBanned(ip string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, banned := m.bannedIPs[ip]
	return banned
}

// IsIPBannedByPolicy applies shared runtime policy rules before checking the ban map.
func IsIPBannedByPolicy(ipFilter *IPFilter, candidate string) bool {
	if ipFilter == nil {
		return false
	}
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	return ipFilter.IsIPBanned(candidate)
}

// GetBannedIPs returns all banned IPs.
func (m *IPFilter) GetBannedIPs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, 0, len(m.bannedIPs))
	for ip := range m.bannedIPs {
		result = append(result, ip)
	}
	return result
}

// SetBannedIPs sets the banned IPs list (for loading from settings).
func (m *IPFilter) SetBannedIPs(ips []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bannedIPs = make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		m.bannedIPs[ip] = struct{}{}
	}
}

// RegisterLeaseIP associates a lease ID with an IP address.
func (m *IPFilter) RegisterLeaseIP(leaseID, ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if leaseID == "" || ip == "" {
		return
	}

	if oldIP, exists := m.leaseToIP[leaseID]; exists {
		if oldIP == ip {
			// Already registered; avoid duplicate lease entries per IP.
			return
		}
		m.removeLeaseFromIP(leaseID, oldIP)
	}

	// Defensively avoid duplicates if state was previously inconsistent.
	if slices.Contains(m.ipToLeases[ip], leaseID) {
		m.leaseToIP[leaseID] = ip
		return
	}

	m.leaseToIP[leaseID] = ip
	m.ipToLeases[ip] = append(m.ipToLeases[ip], leaseID)
}

// removeLeaseFromIP removes a lease from IP's lease list (must hold lock).
func (m *IPFilter) removeLeaseFromIP(leaseID, ip string) {
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
func (m *IPFilter) GetLeaseIP(leaseID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.leaseToIP[leaseID]
}

// GetIPLeases returns all lease IDs for an IP.
func (m *IPFilter) GetIPLeases(ip string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]string, len(m.ipToLeases[ip]))
	copy(result, m.ipToLeases[ip])
	return result
}

// RemoveLeaseIP removes lease-to-IP mapping for a lease ID.
func (m *IPFilter) RemoveLeaseIP(leaseID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ip, exists := m.leaseToIP[leaseID]
	if !exists {
		return
	}
	delete(m.leaseToIP, leaseID)
	m.removeLeaseFromIP(leaseID, ip)
}

func normalizeClientIPCandidate(raw string) string {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return ""
	}

	if ip := net.ParseIP(candidate); ip != nil {
		return candidate
	}

	host, _, err := net.SplitHostPort(candidate)
	if err != nil {
		return ""
	}
	host = strings.TrimSpace(host)
	if host == "" || net.ParseIP(host) == nil {
		return ""
	}
	return host
}

func parseRemoteAddrIP(remoteAddr string) net.IP {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if remoteAddr == "" {
		return nil
	}

	host := remoteAddr
	if parsedHost, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = parsedHost
	}

	return net.ParseIP(strings.TrimSpace(host))
}

// ExtractClientIP extracts the client IP from an HTTP request.
// Forwarded headers are trusted only when trustProxyHeaders is true and peer is trusted.
func ExtractClientIP(r *http.Request, trustProxyHeaders bool) string {
	if r == nil {
		return ""
	}

	if trustProxyHeaders && IsTrustedProxyRemoteAddr(r.RemoteAddr) {
		// Check X-Forwarded-For header first (for proxied requests).
		if xff := r.Header.Get(xForwardedForHeader); xff != "" {
			// X-Forwarded-For can contain multiple IPs, take the first one.
			if before, _, ok := strings.Cut(xff, ","); ok {
				if ip := normalizeClientIPCandidate(before); ip != "" {
					return ip
				}
			} else if ip := normalizeClientIPCandidate(xff); ip != "" {
				return ip
			}
		}

		// Check X-Real-IP header.
		if xri := r.Header.Get(xRealIPHeader); xri != "" {
			if ip := normalizeClientIPCandidate(xri); ip != "" {
				return ip
			}
		}
	}

	// Fall back to RemoteAddr.
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	if normalized := normalizeClientIPCandidate(ip); normalized != "" {
		return normalized
	}
	return strings.TrimSpace(ip)
}
