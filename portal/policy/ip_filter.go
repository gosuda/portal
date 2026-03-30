package policy

import (
	"slices"
	"strings"
	"sync"
)

type IPFilter struct {
	bannedIPs  map[string]struct{}
	leaseToIP  map[string]string
	ipToLeases map[string][]string
	mu         sync.RWMutex
}

func NewIPFilter() *IPFilter {
	return &IPFilter{
		bannedIPs:  make(map[string]struct{}),
		leaseToIP:  make(map[string]string),
		ipToLeases: make(map[string][]string),
	}
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
	if leaseID == "" {
		return ""
	}
	return f.leaseToIP[leaseID]
}

func (f *IPFilter) RemoveLeaseIP(leaseID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if leaseID == "" {
		return
	}
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
