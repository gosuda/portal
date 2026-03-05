package types

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const defaultBootstrapURL = "https://localhost:4017"

func normalizeRootHost(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.TrimPrefix(strings.TrimSuffix(normalized, "."), "*.")
	return normalized
}

// IsLocalhost reports whether host resolves to localhost/loopback semantics.
// It accepts bare hosts, host:port forms, and bracketed IPv6 literals.
func IsLocalhost(host string) bool {
	normalized := strings.ToLower(strings.TrimSpace(host))
	if normalized == "" {
		return false
	}

	if parsedHost, _, err := net.SplitHostPort(normalized); err == nil {
		normalized = parsedHost
	}
	normalized = strings.TrimPrefix(strings.TrimSuffix(normalized, "."), "*.")
	normalized = strings.TrimPrefix(strings.TrimSuffix(normalized, "]"), "[")

	if normalized == "localhost" || strings.HasSuffix(normalized, ".localhost") {
		return true
	}

	if ip := net.ParseIP(normalized); ip != nil {
		return ip.IsLoopback()
	}
	return false
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

func parsePortalAddress(raw, fallbackScheme string) (scheme, rootHost, hostPort string, ok bool) {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return "", "", "", false
	}

	if fallbackScheme == "" {
		fallbackScheme = "https"
	}
	fallbackScheme = strings.ToLower(strings.TrimSpace(fallbackScheme))

	if !strings.Contains(normalized, "://") {
		normalized = fallbackScheme + "://" + normalized
	}

	parsed, err := url.Parse(normalized)
	if err != nil || parsed.Hostname() == "" {
		return "", "", "", false
	}

	rootHost = normalizeRootHost(parsed.Hostname())
	if rootHost == "" {
		return "", "", "", false
	}

	scheme = strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme == "" {
		scheme = fallbackScheme
	}

	if port := strings.TrimSpace(parsed.Port()); port != "" {
		hostPort = net.JoinHostPort(rootHost, port)
	} else {
		hostPort = rootHost
	}

	return scheme, rootHost, strings.ToLower(strings.TrimSpace(hostPort)), true
}

// NormalizeServiceName canonicalizes and validates a service/lease name for DNS usage.
// Valid names are a single DNS label: [a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?
func NormalizeServiceName(name string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.TrimPrefix(normalized, "*.")
	normalized = strings.TrimSuffix(normalized, ".")

	if normalized == "" || len(normalized) > 63 {
		return "", false
	}
	if strings.Contains(normalized, ".") || normalized[0] == '-' || normalized[len(normalized)-1] == '-' {
		return "", false
	}
	for _, ch := range normalized {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
		case ch == '-':
		default:
			return "", false
		}
	}
	return normalized, true
}

// IsValidServiceName reports whether a service name can be used as a DNS label.
func IsValidServiceName(name string) bool {
	_, ok := NormalizeServiceName(name)
	return ok
}

// DefaultAppPattern builds a wildcard subdomain pattern from a base portal URL or host.
func DefaultAppPattern(base string) string {
	if strings.TrimSpace(base) == "" {
		return "*.localhost:4017"
	}
	_, _, hostPort, ok := parsePortalAddress(base, "https")
	if !ok || hostPort == "" {
		return "*.localhost:4017"
	}
	return "*." + hostPort
}

// ServicePublicURL returns a service URL derived from portalURL and service name.
func ServicePublicURL(portalURL, serviceName string) string {
	normalizedName, ok := NormalizeServiceName(serviceName)
	if !ok {
		return ""
	}

	scheme, rootHost, _, ok := parsePortalAddress(portalURL, "https")
	if !ok || rootHost == "" {
		return ""
	}
	return fmt.Sprintf("%s://%s.%s", scheme, normalizedName, rootHost)
}

// PortalHostPort returns normalized host[:port] from a portal URL-like input.
func PortalHostPort(portalURL string) string {
	_, _, hostPort, ok := parsePortalAddress(portalURL, "https")
	if !ok {
		return ""
	}
	return hostPort
}

// PortalRootHost extracts the root hostname from a portal URL.
func PortalRootHost(portalURL string) string {
	_, rootHost, _, ok := parsePortalAddress(portalURL, "https")
	if !ok {
		return ""
	}
	return rootHost
}

// DefaultBootstrapFrom derives a relay API bootstrap URL from a base portal URL or host.
func DefaultBootstrapFrom(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return defaultBootstrapURL
	}

	if !strings.Contains(base, "://") {
		base = "https://" + base
	}

	u, err := url.Parse(strings.TrimSuffix(base, "/"))
	if err != nil || u.Host == "" {
		return defaultBootstrapURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return defaultBootstrapURL
	}
	if p := strings.TrimSpace(u.Path); p != "" && p != "/" {
		return defaultBootstrapURL
	}

	u.Scheme = "https"
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
	normalizedLeaseName, ok := NormalizeServiceName(leaseName)
	if !ok {
		return "", false
	}

	return normalizedLeaseName, true
}

// BuildSNIName constructs the SNI hostname for a lease.
func BuildSNIName(leaseName, baseHost string) string {
	normalizedLeaseName, ok := NormalizeServiceName(leaseName)
	if !ok {
		return ""
	}

	normalizedBaseHost := PortalRootHost(baseHost)
	if normalizedBaseHost == "" {
		normalizedBaseHost = normalizeRootHost(baseHost)
	}
	if normalizedBaseHost == "" {
		return ""
	}

	return normalizedLeaseName + "." + normalizedBaseHost
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

	var port string
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
		return "", errors.New("empty host")
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
		return "", errors.New("missing host in URL")
	}
	return u.Host, nil
}

// NormalizeRelayAPIURL normalizes a relay API URL.
// It accepts host:port input (defaults to https), validates the scheme,
// normalizes localhost hostnames, and removes path/query/fragment.
func NormalizeRelayAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty relay URL")
	}

	// Accept host:port input.
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
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
	case "https":
	default:
		return "", fmt.Errorf("unsupported relay URL scheme: %q (use https)", u.Scheme)
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
		return nil, errors.New("no available relay")
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
		return nil, errors.New("no available relay")
	}
	return out, nil
}
