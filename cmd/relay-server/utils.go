package main

import (
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"gosuda.org/portal/portal"
)

// URL-safe name validation regex
var urlSafeNameRegex = regexp.MustCompile(`^[\p{L}\p{N}_-]+$`)

// isURLSafeName checks if a name contains only URL-safe characters.
func isURLSafeName(name string) bool {
	if name == "" {
		return true
	}
	return urlSafeNameRegex.MatchString(name)
}

// normalizePortalURL takes various user-friendly server inputs and
// converts them into a relay API base URL.
func normalizePortalURL(raw string) (string, error) {
	server := strings.TrimSpace(raw)
	if server == "" {
		return "", fmt.Errorf("bootstrap server is empty")
	}

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

// parseURLs splits a comma-separated string into a list of trimmed, non-empty URLs.
func parseURLs(raw string) []string {
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

// isHexString reports whether s contains only hexadecimal characters
func isHexString(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// isSubdomain reports whether host matches the given domain pattern.
func isSubdomain(domain, host string) bool {
	if host == "" || domain == "" {
		return false
	}

	h := strings.ToLower(stripPort(stripScheme(host)))
	d := strings.ToLower(stripPort(stripScheme(domain)))

	if strings.HasPrefix(d, "*.") {
		suffix := d[1:]
		return len(h) > len(suffix) && strings.HasSuffix(h, suffix)
	}

	if h == d {
		return true
	}

	return strings.HasSuffix(h, "."+d)
}

func stripScheme(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	return s
}

func stripWildCard(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "*.")
	return s
}

func stripPort(s string) string {
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

// defaultAppPattern builds a wildcard subdomain pattern from a base portal URL or host.
func defaultAppPattern(base string) string {
	base = strings.TrimSpace(strings.TrimSuffix(base, "/"))
	if base == "" {
		return "*.localhost:4017"
	}
	host := stripWildCard(stripScheme(base))
	if host == "" {
		return "*.localhost:4017"
	}
	if strings.HasPrefix(host, "*.") {
		return host
	}
	return "*." + host
}

// defaultBootstrapFrom derives a relay API bootstrap URL from a base portal URL or host.
func defaultBootstrapFrom(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return "http://localhost:4017"
	}
	if u, err := normalizePortalURL(base); err == nil && u != "" {
		return u
	}

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

// portalHostPort returns normalized host[:port] from a portal URL-like input.
func portalHostPort(portalURL string) string {
	return strings.ToLower(strings.TrimSpace(
		stripWildCard(stripScheme(portalURL)),
	))
}

// portalBaseHostNoPort returns host without port from a portal URL-like input.
func portalBaseHostNoPort(portalURL string) string {
	return strings.ToLower(strings.TrimSpace(stripPort(portalHostPort(portalURL))))
}

// servicePublicURL returns a service URL derived from portalURL and service name.
func servicePublicURL(portalURL, serviceName string) string {
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

	host := strings.TrimSpace(stripWildCard(u.Host))
	if host == "" {
		return ""
	}

	scheme := strings.TrimSpace(u.Scheme)
	if scheme == "" {
		scheme = "http"
	}

	return fmt.Sprintf("%s://%s.%s", scheme, serviceName, host)
}

// isHTMLContentType checks if the Content-Type header indicates HTML content
func isHTMLContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.HasPrefix(strings.ToLower(contentType), "text/html")
	}
	return mediaType == "text/html"
}

// getContentType returns the MIME type for a file extension
func getContentType(ext string) string {
	switch ext {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".wasm":
		return "application/wasm"
	case ".css":
		return "text/css"
	case ".mp4":
		return "video/mp4"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	default:
		return ""
	}
}

// setCORSHeaders sets permissive CORS headers for GET/OPTIONS and common headers
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Accept-Encoding")
}

func isLocalhost(r *http.Request) bool {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}

	if strings.EqualFold(host, "host.docker.internal") {
		return true
	}

	ip := net.ParseIP(host)
	if ip == nil {
		if addrs, err := net.LookupIP(host); err == nil {
			for _, a := range addrs {
				if a.IsLoopback() || a.IsPrivate() {
					return true
				}
			}
		}
		return false
	}

	return ip.IsLoopback() || ip.IsPrivate()
}

// extractBaseDomain extracts the base domain from a URL.
// For example, "https://app.portal.com" -> "portal.com"
func extractBaseDomain(portalURL string) string {
	portalURL = strings.TrimSpace(portalURL)
	if portalURL == "" {
		return ""
	}

	// Remove scheme if present
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(strings.ToLower(portalURL), prefix) {
			portalURL = portalURL[len(prefix):]
			break
		}
	}

	// Remove port if present
	if idx := strings.Index(portalURL, ":"); idx > 0 {
		portalURL = portalURL[:idx]
	}

	// Remove path if present
	if idx := strings.Index(portalURL, "/"); idx > 0 {
		portalURL = portalURL[:idx]
	}

	// Remove wildcard if present
	portalURL = strings.TrimPrefix(portalURL, "*.")

	// Extract base domain (last two parts)
	parts := strings.Split(portalURL, ".")
	if len(parts) < 2 {
		return ""
	}

	// Return last two parts
	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}

// leaseNameFromHost extracts the lease name from a subdomain host.
// It returns the lease name and true if the host is a valid subdomain of appURL.
func leaseNameFromHost(host, appURL string) (string, bool) {
	if !isSubdomain(appURL, host) {
		return "", false
	}

	normalizedHost := strings.ToLower(strings.TrimSpace(stripPort(host)))
	baseHost := strings.ToLower(strings.TrimSpace(
		stripPort(stripWildCard(stripScheme(appURL))),
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
		// Lease names do not include dots; avoid ambiguous nested subdomains.
		return "", false
	}

	return leaseName, true
}

// redirectToHTTPS redirects the request to HTTPS using configured SNI port.
func redirectToHTTPS(w http.ResponseWriter, r *http.Request, sniListenAddr string) {
	targetHost := hostForHTTPSRedirect(r.Host, sniListenAddr)
	target := "https://" + targetHost + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

func hostForHTTPSRedirect(requestHost, sniListenAddr string) string {
	host := strings.TrimSpace(requestHost)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}

	port := tlsPortForRedirect(sniListenAddr)
	if port == "443" {
		return host
	}

	return net.JoinHostPort(host, port)
}

func tlsPortForRedirect(sniListenAddr string) string {
	raw := strings.TrimSpace(sniListenAddr)
	if raw == "" {
		return "443"
	}

	port := ""
	switch {
	case strings.HasPrefix(raw, ":"):
		port = strings.TrimPrefix(raw, ":")
	case strings.Count(raw, ":") == 0:
		port = raw
	default:
		_, parsedPort, err := net.SplitHostPort(raw)
		if err != nil {
			return "443"
		}
		port = parsedPort
	}

	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "443"
	}
	return port
}

// openLeaseConnection acquires a reverse connection for the given lease ID.
func openLeaseConnection(leaseID string, serv *portal.RelayServer) (net.Conn, func(), error) {
	reverseConn, err := serv.GetReverseHub().AcquireStarted(leaseID, portal.ReverseHTTPWait)
	if err != nil {
		return nil, nil, fmt.Errorf("no reverse connection available for lease %s: %w", leaseID, err)
	}
	return reverseConn.Conn, reverseConn.Close, nil
}

// withCORSMiddleware wraps a handler with CORS headers.
func withCORSMiddleware(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		h(w, r)
	}
}
