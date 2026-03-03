package types

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ExtractBaseDomain extracts the base domain (e.g., "example.com") from a URL.
// Returns empty string if the URL is invalid or has fewer than 2 domain parts.
func ExtractBaseDomain(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}

	u, err := url.Parse(trimmed)
	if err != nil || u.Hostname() == "" {
		return ""
	}

	host := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(u.Hostname())), "*.")
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}

// StripScheme removes http:// or https:// prefix from a string.
func StripScheme(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	return s
}

// StripWildcard removes *. prefix from a domain pattern.
func StripWildcard(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "*.")
	return s
}

// StripPort removes a trailing :port from a host string if present.
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

// IsSubdomain reports whether host matches the given domain pattern.
// Pattern can be a wildcard like "*.example.com" or exact domain.
func IsSubdomain(domain, host string) bool {
	if host == "" || domain == "" {
		return false
	}

	h := strings.ToLower(StripPort(StripScheme(host)))
	d := strings.ToLower(StripPort(StripScheme(domain)))

	if strings.HasPrefix(d, "*.") {
		suffix := d[1:]
		return len(h) > len(suffix) && strings.HasSuffix(h, suffix)
	}

	if h == d {
		return true
	}

	return strings.HasSuffix(h, "."+d)
}

// DefaultAppPattern builds a wildcard subdomain pattern from a base portal URL or host.
func DefaultAppPattern(base string) string {
	base = strings.TrimSpace(strings.TrimSuffix(base, "/"))
	if base == "" {
		return "*.localhost:4017"
	}
	host := StripWildcard(StripScheme(base))
	if host == "" {
		return "*.localhost:4017"
	}
	if strings.HasPrefix(host, "*.") {
		return host
	}
	return "*." + host
}

// ServicePublicURL returns a service URL derived from portalURL and service name.
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

	host := strings.TrimSpace(StripWildcard(u.Host))
	if host == "" {
		return ""
	}

	scheme := strings.TrimSpace(u.Scheme)
	if scheme == "" {
		scheme = "http"
	}

	return fmt.Sprintf("%s://%s.%s", scheme, serviceName, host)
}

// PortalHostPort returns normalized host[:port] from a portal URL-like input.
func PortalHostPort(portalURL string) string {
	return strings.ToLower(strings.TrimSpace(
		StripWildcard(StripScheme(portalURL)),
	))
}

// PortalRootHost extracts the root hostname from a portal URL.
func PortalRootHost(portalURL string) string {
	raw := strings.TrimSpace(portalURL)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(parsed.Hostname())), "*.")
}

// DefaultBootstrapFrom derives a relay API bootstrap URL from a base portal URL or host.
func DefaultBootstrapFrom(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return "http://localhost:4017"
	}

	if !strings.Contains(base, "://") {
		base = "http://" + base
	}

	u, err := url.Parse(strings.TrimSuffix(base, "/"))
	if err != nil || u.Host == "" {
		return "http://localhost:4017"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "http://localhost:4017"
	}
	if p := strings.TrimSpace(u.Path); p != "" && p != "/" {
		return "http://localhost:4017"
	}

	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimSuffix(u.String(), "/")
}

// LeaseNameFromHost extracts the lease name from a subdomain host.
func LeaseNameFromHost(host, appURL string) (string, bool) {
	if !IsSubdomain(appURL, host) {
		return "", false
	}

	normalizedHost := strings.ToLower(strings.TrimSpace(StripPort(host)))
	baseHost := strings.ToLower(strings.TrimSpace(
		StripPort(StripWildcard(StripScheme(appURL))),
	))

	if normalizedHost == "" || baseHost == "" || normalizedHost == baseHost {
		return "", false
	}

	suffix := "." + baseHost
	if !strings.HasSuffix(normalizedHost, suffix) {
		return "", false
	}

	leaseName := strings.TrimSuffix(normalizedHost, suffix)
	if leaseName == "" || strings.Contains(leaseName, ".") {
		return "", false
	}

	return leaseName, true
}

// BuildSNIName constructs the SNI hostname for a lease.
func BuildSNIName(leaseName, baseHost string) string {
	leaseName = strings.ToLower(strings.TrimSpace(leaseName))
	baseHost = strings.TrimSpace(baseHost)
	if leaseName == "" || baseHost == "" {
		return ""
	}
	return leaseName + "." + baseHost
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

// ParsePortNumber parses a port number from a string, returning fallback on error.
// The raw value may optionally start with a colon prefix.
func ParsePortNumber(raw string, fallback int) int {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback
	}
	value = strings.TrimPrefix(value, ":")
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return fallback
	}
	return port
}

// LoopbackForwardAddr converts a listen address to a loopback forward address.
// For example, ":4017" becomes "127.0.0.1:4017".
func LoopbackForwardAddr(listenAddr string) string {
	raw := strings.TrimSpace(listenAddr)
	if raw == "" {
		return ""
	}

	port := ""
	switch {
	case strings.HasPrefix(raw, ":"):
		port = strings.TrimPrefix(raw, ":")
	case strings.Count(raw, ":") == 0:
		port = raw
	default:
		_, p, err := net.SplitHostPort(raw)
		if err != nil {
			return ""
		}
		port = p
	}

	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1 || portNum > 65535 {
		return ""
	}

	return net.JoinHostPort("127.0.0.1", strconv.Itoa(portNum))
}

// NormalizeTargetAddr normalizes a target address for dialing.
// If the input is a URL (e.g., "http://localhost:8080"), it extracts the host.
// Otherwise, it returns the trimmed input.
func NormalizeTargetAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty host")
	}

	// Treat plain host[:port] as a dial target without URL parsing.
	if !strings.Contains(raw, "://") {
		return raw, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse target URL: %w", err)
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("missing host in URL")
	}
	return u.Host, nil
}

// NormalizeRelayAPIURL normalizes a relay API URL.
// It accepts host:port input (defaults to http), validates the scheme,
// normalizes localhost hostnames, and removes path/query/fragment.
func NormalizeRelayAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty relay URL")
	}

	// Accept host:port input.
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse relay URL: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("relay URL missing host: %q", raw)
	}

	if host := strings.ToLower(strings.TrimSpace(u.Hostname())); strings.HasSuffix(host, ".localhost") {
		port := u.Port()
		if port != "" {
			u.Host = net.JoinHostPort("localhost", port)
		} else {
			u.Host = "localhost"
		}
	}

	switch u.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("unsupported relay URL scheme: %q (use http/https)", u.Scheme)
	}

	if p := strings.TrimSpace(u.Path); p != "" && p != "/" {
		return "", fmt.Errorf("relay URL must not include path: %q", raw)
	}

	u.RawQuery = ""
	u.Fragment = ""
	u.Path = ""

	return strings.TrimSuffix(u.String(), "/"), nil
}

// NormalizeRelayAPIURLs normalizes a list of relay API URLs, deduplicating results.
// Returns an error if no valid URLs remain after normalization.
func NormalizeRelayAPIURLs(bootstrapServers []string) ([]string, error) {
	if len(bootstrapServers) == 0 {
		return nil, fmt.Errorf("no available relay")
	}

	seen := make(map[string]struct{}, len(bootstrapServers))
	out := make([]string, 0, len(bootstrapServers))
	for _, relay := range bootstrapServers {
		normalized, err := NormalizeRelayAPIURL(relay)
		if err != nil {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no available relay")
	}
	return out, nil
}
