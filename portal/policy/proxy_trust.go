package policy

import (
	"net"
	"net/http"
	"strings"
)

var defaultTrustedProxyCIDRs = mustParseTrustedProxyCIDRs(
	"127.0.0.0/8",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16",
	"100.64.0.0/10",
	"::1/128",
	"fc00::/7",
	"fe80::/10",
)

func IsTrustedProxyRemoteAddr(remoteAddr string, trustedProxyCIDRs []*net.IPNet) bool {
	remoteIP := parseRemoteAddrIP(remoteAddr)
	if remoteIP == nil {
		return false
	}

	networks := trustedProxyCIDRs
	if len(networks) == 0 {
		networks = defaultTrustedProxyCIDRs
	}
	for _, network := range networks {
		if network != nil && network.Contains(remoteIP) {
			return true
		}
	}
	return false
}

func ExtractClientIP(r *http.Request, trustProxyHeaders bool, trustedProxyCIDRs []*net.IPNet) string {
	if r == nil {
		return ""
	}

	if trustProxyHeaders && IsTrustedProxyRemoteAddr(r.RemoteAddr, trustedProxyCIDRs) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if before, _, ok := strings.Cut(xff, ","); ok {
				if ip := normalizeClientIPCandidate(before); ip != "" {
					return ip
				}
			} else if ip := normalizeClientIPCandidate(xff); ip != "" {
				return ip
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
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

func IsSecureForwardedRequest(r *http.Request, trustProxyHeaders bool, trustedProxyCIDRs []*net.IPNet) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	if !trustProxyHeaders || !IsTrustedProxyRemoteAddr(r.RemoteAddr, trustedProxyCIDRs) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
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

func mustParseTrustedProxyCIDRs(values ...string) []*net.IPNet {
	cidrs := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			panic(err)
		}
		cidrs = append(cidrs, network)
	}
	return cidrs
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
