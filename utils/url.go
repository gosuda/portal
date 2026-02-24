package utils

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// URL-safe name validation regex
var urlSafeNameRegex = regexp.MustCompile(`^[\p{L}\p{N}_-]+$`)

// IsURLSafeName checks if a name contains only URL-safe characters.
// Disallows: spaces, special characters like /, ?, &, =, %, etc.
// Note: Browsers will automatically URL-encode non-ASCII characters.
func IsURLSafeName(name string) bool {
	if name == "" {
		return true // Empty name is allowed (will be treated as unnamed)
	}
	return urlSafeNameRegex.MatchString(name)
}

// NormalizePortalURL takes various user-friendly server inputs and
// converts them into a relay API base URL.
// Examples:
//   - "http://example.com"  -> "http://example.com"
//   - "https://example.com" -> "https://example.com"
//   - "localhost:4017"      -> "http://localhost:4017"
//   - "example.com"         -> "http://example.com"
func NormalizePortalURL(raw string) (string, error) {
	server := strings.TrimSpace(raw)
	if server == "" {
		return "", fmt.Errorf("bootstrap server is empty")
	}

	// Accept host:port input.
	if !strings.Contains(server, "://") {
		server = "http://" + server
	}

	u, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("invalid bootstrap server %q: %w", raw, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid bootstrap server %q: missing host", raw)
	}

	switch u.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("invalid bootstrap server %q: unsupported scheme %q (use http/https)", raw, u.Scheme)
	}

	if p := strings.TrimSpace(u.Path); p != "" && p != "/" {
		return "", fmt.Errorf("invalid bootstrap server %q: path is not allowed", raw)
	}

	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimSuffix(u.String(), "/"), nil
}

// ParseURLs splits a comma-separated string into a list of trimmed, non-empty URLs.
func ParseURLs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// IsHexString reports whether s contains only hexadecimal characters
func IsHexString(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

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

// DefaultBootstrapFrom derives a relay API bootstrap URL from a base portal URL or host.
// It prefers NormalizePortalURL for consistent mapping and falls back to localhost.
// Examples:
//   - "https://portal.example.com" -> "https://portal.example.com"
//   - "http://portal.example.com"  -> "http://portal.example.com"
//   - "localhost:4017"             -> "http://localhost:4017"
//   - ""                           -> "http://localhost:4017"
func DefaultBootstrapFrom(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return "http://localhost:4017"
	}
	if u, err := NormalizePortalURL(base); err == nil && u != "" {
		return u
	}

	// Fallback for non-standard input while keeping api-base format.
	if strings.Contains(base, "://") {
		return "http://localhost:4017"
	}
	u, err := url.Parse("http://" + strings.TrimSuffix(base, "/"))
	if err != nil || u.Host == "" {
		return "http://localhost:4017"
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimSuffix(u.String(), "/")
}

// PortalHostPort returns normalized host[:port] from a portal URL-like input.
// Examples:
//   - "https://Portal.Example.com" -> "portal.example.com"
//   - "http://portal.example.com:4017" -> "portal.example.com:4017"
func PortalHostPort(portalURL string) string {
	return strings.ToLower(strings.TrimSpace(
		StripWildCard(StripScheme(portalURL)),
	))
}

// PortalBaseHostNoPort returns host without port from a portal URL-like input.
// Examples:
//   - "https://portal.example.com:4017" -> "portal.example.com"
func PortalBaseHostNoPort(portalURL string) string {
	return strings.ToLower(strings.TrimSpace(StripPort(PortalHostPort(portalURL))))
}

// ServicePublicURL returns a service URL derived from portalURL and service name.
// Examples:
//   - portalURL: "https://portal.example.com", serviceName: "demo"
//     -> "https://demo.portal.example.com"
func ServicePublicURL(portalURL, serviceName string) string {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return ""
	}

	raw := strings.TrimSpace(portalURL)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil || strings.TrimSpace(u.Host) == "" {
		return ""
	}

	host := strings.TrimSpace(StripWildCard(u.Host))
	if host == "" {
		return ""
	}

	scheme := strings.TrimSpace(u.Scheme)
	if scheme == "" {
		scheme = "http"
	}

	return fmt.Sprintf("%s://%s.%s", scheme, serviceName, host)
}
