package portalnet

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// URL-safe name validation regex.
var urlSafeNameRegex = regexp.MustCompile(`^[\p{L}\p{N}_-]+$`)

// IsURLSafeName checks if a name contains only URL-safe characters.
// Disallows: spaces, special characters like /, ?, &, =, %, etc.
// Note: Browsers will automatically URL-encode non-ASCII characters.
func IsURLSafeName(name string) bool {
	return name == "" || urlSafeNameRegex.MatchString(name) // Empty name is allowed (will be treated as unnamed)
}

// NormalizePortalURL takes various user-friendly server inputs and
// converts them into a proper HTTPS URL for WebTransport.
// Legacy ws:// and wss:// schemes are converted to http:// and https://.
// Examples:
//   - "https://example.com"       -> "https://example.com/relay"
//   - "http://localhost:4017"     -> "http://localhost:4017/relay"
//   - "wss://example.com/relay"  -> "https://example.com/relay"
//   - "ws://localhost:4017/relay" -> "http://localhost:4017/relay"
//   - "localhost:4017"            -> "https://localhost:4017/relay"
//   - "example.com"               -> "https://example.com/relay"
func NormalizePortalURL(raw string) (string, error) {
	server := strings.TrimSpace(raw)
	if server == "" {
		return "", errors.New("bootstrap server is empty")
	}

	setRelayPathIfEmpty := func(u *url.URL) {
		if u.Path == "" || u.Path == "/" {
			u.Path = "/relay"
		}
	}

	// Convert legacy WebSocket schemes to HTTP equivalents
	server = strings.Replace(server, "wss://", "https://", 1)
	server = strings.Replace(server, "ws://", "http://", 1)

	// Already an HTTP(S) URL
	if strings.HasPrefix(server, "http://") || strings.HasPrefix(server, "https://") {
		u, err := url.Parse(server)
		if err != nil {
			return "", fmt.Errorf("invalid bootstrap server %q: %w", raw, err)
		}
		setRelayPathIfEmpty(u)
		return u.String(), nil
	}

	// Bare host[:port][/path] -> assume HTTPS and /relay if no path
	u, err := url.Parse("https://" + server)
	if err != nil {
		return "", fmt.Errorf("invalid bootstrap server %q: %w", raw, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid bootstrap server %q: missing host", raw)
	}
	setRelayPathIfEmpty(u)
	return u.String(), nil
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

// IsHexString reports whether s contains only hexadecimal characters.
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

// DefaultBootstrapFrom derives a bootstrap URL from a base portal URL or host.
// It prefers NormalizePortalURL for consistent mapping and falls back to localhost.
// Examples:
//   - "https://portal.example.com" -> "https://portal.example.com/relay"
//   - "http://portal.example.com"  -> "http://portal.example.com/relay"
//   - "localhost:4017"             -> "https://localhost:4017/relay"
//   - ""                           -> "http://localhost:4017/relay"
func DefaultBootstrapFrom(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return "http://localhost:4017/relay"
	}
	if u, err := NormalizePortalURL(base); err == nil && u != "" {
		return u
	}
	host := StripScheme(strings.TrimSuffix(base, "/"))
	if host == "" {
		return "http://localhost:4017/relay"
	}
	return "https://" + host + "/relay"
}
