package utils

import (
	"strings"
)

// IsSubdomain reports whether host matches the given domain pattern.
// Supports patterns like:
//   - "*.example.com" (wildcard for any subdomain of example.com)
//   - "sub.example.com" (exact host match)
//
// Normalizes by stripping scheme/port and lowercasing.
func IsSubdomain(domain, host string) bool {
	if host == "" || domain == "" {
		return false
	}

	h := strings.ToLower(StripPort(StripScheme(host)))
	d := strings.ToLower(StripPort(StripScheme(domain)))

	// Wildcard pattern: require at least one label before the suffix
	if strings.HasPrefix(d, "*.") {
		suffix := d[1:] // keep leading dot (e.g., ".example.com")
		return len(h) > len(suffix) && strings.HasSuffix(h, suffix)
	}

	if h == d {
		return true
	}

	return strings.HasSuffix(h, "."+d)
}

func StripScheme(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")

	return s
}

func StripWildCard(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "*.")
	return s
}

func StripPort(s string) string {
	if s == "" {
		return s
	}
	if idx := strings.LastIndexByte(s, ':'); idx >= 0 && idx+1 < len(s) {
		port := s[idx+1:]
		digits := true
		for _, ch := range port {
			if ch < '0' || ch > '9' {
				digits = false
				break
			}
		}
		if digits {
			return s[:idx]
		}
	}
	return s
}

// DefaultAppPattern builds a wildcard subdomain pattern from a base portal URL or host.
// Examples:
//   - "https://portal.example.com" -> "*.portal.example.com"
//   - "portal.example.com"        -> "*.portal.example.com"
//   - "localhost:4017"            -> "*.localhost:4017"
//   - ""                          -> "*.localhost:4017"
func DefaultAppPattern(base string) string {
	base = strings.TrimSpace(strings.TrimSuffix(base, "/"))
	if base == "" {
		return "*.localhost:4017"
	}
	host := StripWildCard(StripScheme(base))
	if host == "" {
		return "*.localhost:4017"
	}
	// Avoid doubling wildcard if provided accidentally
	if strings.HasPrefix(host, "*.") {
		return host
	}
	return "*." + host
}
