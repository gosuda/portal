package policy

import (
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
)

const (
	xForwardedForHeader = "X-Forwarded-For"
	xRealIPHeader       = "X-Real-IP"
	xForwardedProto     = "X-Forwarded-Proto"
)

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

func NewIPFilter() *IPFilter {
	return &IPFilter{
		bannedIPs:  make(map[string]struct{}),
		leaseToIP:  make(map[string]string),
		ipToLeases: make(map[string][]string),
	}
}

func ParseTrustedProxyCIDRs(raw string) ([]*net.IPNet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	cidrs := make([]*net.IPNet, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, network, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", part, err)
		}
		key := network.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		cidrs = append(cidrs, network)
	}
	return cidrs, nil
}

func SetTrustedProxyCIDRs(cidrs []*net.IPNet) {
	trustedProxyMu.Lock()
	defer trustedProxyMu.Unlock()
	if len(cidrs) == 0 {
		trustedProxyCIDRs = nil
		return
	}
	trustedProxyCIDRs = append(make([]*net.IPNet, 0, len(cidrs)), cidrs...)
}

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

func ExtractClientIP(r *http.Request, trustProxyHeaders bool) string {
	if r == nil {
		return ""
	}

	if trustProxyHeaders && IsTrustedProxyRemoteAddr(r.RemoteAddr) {
		if xff := r.Header.Get(xForwardedForHeader); xff != "" {
			if before, _, ok := strings.Cut(xff, ","); ok {
				if ip := normalizeClientIPCandidate(before); ip != "" {
					return ip
				}
			} else if ip := normalizeClientIPCandidate(xff); ip != "" {
				return ip
			}
		}
		if xri := r.Header.Get(xRealIPHeader); xri != "" {
			if ip := normalizeClientIPCandidate(xri); ip != "" {
				return ip
			}
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	if normalized := normalizeClientIPCandidate(host); normalized != "" {
		return normalized
	}
	return strings.TrimSpace(host)
}

func IsSecureForwardedRequest(r *http.Request, trustProxyHeaders bool) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	if !trustProxyHeaders || !IsTrustedProxyRemoteAddr(r.RemoteAddr) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get(xForwardedProto)), "https")
}

func (f *IPFilter) BanIP(ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bannedIPs[strings.TrimSpace(ip)] = struct{}{}
}

func (f *IPFilter) UnbanIP(ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.bannedIPs, strings.TrimSpace(ip))
}

func (f *IPFilter) IsIPBanned(ip string) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	_, ok := f.bannedIPs[strings.TrimSpace(ip)]
	return ok
}

func (f *IPFilter) BannedIPs() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]string, 0, len(f.bannedIPs))
	for ip := range f.bannedIPs {
		out = append(out, ip)
	}
	return out
}

func (f *IPFilter) SetBannedIPs(ips []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bannedIPs = make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		f.bannedIPs[ip] = struct{}{}
	}
}

func (f *IPFilter) RegisterLeaseIP(leaseID, ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	leaseID = strings.TrimSpace(leaseID)
	ip = strings.TrimSpace(ip)
	if leaseID == "" || ip == "" {
		return
	}

	if oldIP, ok := f.leaseToIP[leaseID]; ok {
		if oldIP == ip {
			return
		}
		f.removeLeaseFromIPLocked(leaseID, oldIP)
	}
	if slices.Contains(f.ipToLeases[ip], leaseID) {
		f.leaseToIP[leaseID] = ip
		return
	}

	f.leaseToIP[leaseID] = ip
	f.ipToLeases[ip] = append(f.ipToLeases[ip], leaseID)
}

func (f *IPFilter) LeaseIP(leaseID string) string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.leaseToIP[strings.TrimSpace(leaseID)]
}

func (f *IPFilter) RemoveLeaseIP(leaseID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	leaseID = strings.TrimSpace(leaseID)
	ip, ok := f.leaseToIP[leaseID]
	if !ok {
		return
	}
	delete(f.leaseToIP, leaseID)
	f.removeLeaseFromIPLocked(leaseID, ip)
}

func (f *IPFilter) removeLeaseFromIPLocked(leaseID, ip string) {
	leases := f.ipToLeases[ip]
	for i, candidate := range leases {
		if candidate == leaseID {
			f.ipToLeases[ip] = append(leases[:i], leases[i+1:]...)
			break
		}
	}
	if len(f.ipToLeases[ip]) == 0 {
		delete(f.ipToLeases, ip)
	}
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
