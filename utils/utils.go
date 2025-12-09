package utils

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal/utils/wsstream"
)

// SetTCPNoDelay enables TCP_NODELAY on a TCP connection to disable Nagle's algorithm.
// Returns nil for non-TCP connections (e.g., Unix sockets, WebSocket over WASM).
func SetTCPNoDelay(conn net.Conn) error {
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		return tcpConn.SetNoDelay(true)
	}
	return nil
}

// TCPNoDelayListener wraps a net.Listener to enable TCP_NODELAY on accepted connections.
type TCPNoDelayListener struct {
	net.Listener
}

// Accept accepts a connection and enables TCP_NODELAY.
func (l *TCPNoDelayListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if err := SetTCPNoDelay(conn); err != nil {
		log.Debug().Err(err).Msg("failed to set TCP_NODELAY on accepted connection")
	}
	return conn, nil
}

// NewTCPNoDelayListener wraps a listener to enable TCP_NODELAY on accepted connections.
func NewTCPNoDelayListener(l net.Listener) *TCPNoDelayListener {
	return &TCPNoDelayListener{Listener: l}
}

// NewWebSocketDialer returns a dialer that establishes WebSocket connections
// and wraps them as io.ReadWriteCloser. TCP_NODELAY is enabled on the underlying
// TCP connection to minimize latency for interactive relay protocols.
func NewWebSocketDialer() func(context.Context, string) (io.ReadWriteCloser, error) {
	dialer := &websocket.Dialer{
		NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := &net.Dialer{}
			conn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if err := SetTCPNoDelay(conn); err != nil {
				log.Debug().Err(err).Msg("failed to set TCP_NODELAY on WebSocket connection")
			}
			return conn, nil
		},
		HandshakeTimeout: websocket.DefaultDialer.HandshakeTimeout,
	}

	return func(ctx context.Context, url string) (io.ReadWriteCloser, error) {
		wsConn, _, err := dialer.DialContext(ctx, url, nil)
		if err != nil {
			return nil, err
		}
		return &wsstream.WsStream{Conn: wsConn}, nil
	}
}

// defaultWebSocketUpgrader provides a permissive upgrader used across cmd binaries
var defaultWebSocketUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// UpgradeWebSocket upgrades the request/response to a WebSocket connection using DefaultWebSocketUpgrader
func UpgradeWebSocket(w http.ResponseWriter, r *http.Request, responseHeader http.Header) (*websocket.Conn, error) {
	return defaultWebSocketUpgrader.Upgrade(w, r, responseHeader)
}

// UpgradeToWSStream upgrades HTTP to WebSocket and wraps it as io.ReadWriteCloser
func UpgradeToWSStream(w http.ResponseWriter, r *http.Request, responseHeader http.Header) (io.ReadWriteCloser, *websocket.Conn, error) {
	wsConn, err := UpgradeWebSocket(w, r, responseHeader)
	if err != nil {
		return nil, nil, err
	}
	return &wsstream.WsStream{Conn: wsConn}, wsConn, nil
}

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
// converts them into a proper WebSocket URL.
// Examples:
//   - "wss://localhost:4017/relay" -> unchanged
//   - "ws://localhost:4017/relay"  -> unchanged
//   - "http://example.com"        -> "ws://example.com/relay"
//   - "https://example.com"       -> "wss://example.com/relay"
//   - "localhost:4017"            -> "wss://localhost:4017/relay"
//   - "example.com"               -> "wss://example.com/relay"
func NormalizePortalURL(raw string) (string, error) {
	server := strings.TrimSpace(raw)
	if server == "" {
		return "", fmt.Errorf("bootstrap server is empty")
	}

	// Already a WebSocket URL
	if strings.HasPrefix(server, "ws://") || strings.HasPrefix(server, "wss://") {
		return server, nil
	}

	// HTTP/HTTPS -> WS/WSS with default /relay path
	if strings.HasPrefix(server, "http://") || strings.HasPrefix(server, "https://") {
		u, err := url.Parse(server)
		if err != nil {
			return "", fmt.Errorf("invalid bootstrap server %q: %w", raw, err)
		}
		switch u.Scheme {
		case "http":
			u.Scheme = "ws"
		case "https":
			u.Scheme = "wss"
		}
		if u.Path == "" || u.Path == "/" {
			u.Path = "/relay"
		}
		return u.String(), nil
	}

	// Bare host[:port][/path] -> assume WSS and /relay if no path
	u, err := url.Parse("wss://" + server)
	if err != nil {
		return "", fmt.Errorf("invalid bootstrap server %q: %w", raw, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid bootstrap server %q: missing host", raw)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/relay"
	}
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

// GetContentType returns the MIME type for a file extension
func GetContentType(ext string) string {
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

// MatchesWildcardPattern checks if a host matches a wildcard pattern (e.g., *.localhost:4017)
func MatchesWildcardPattern(host, pattern string) bool {
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*")
		return strings.HasSuffix(host, suffix)
	}
	return host == pattern
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

// IsHTMLContentType checks if the Content-Type header indicates HTML content
// It properly handles media type parsing with parameters like charset
func IsHTMLContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.HasPrefix(strings.ToLower(contentType), "text/html")
	}
	return mediaType == "text/html"
}

// SetCORSHeaders sets permissive CORS headers for GET/OPTIONS and common headers
func SetCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Accept-Encoding")
}

// ForwardedHost returns the host from X-Forwarded-Host (first value) or falls back to r.Host.
func ForwardedHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		parts := strings.Split(h, ",")
		return strings.TrimSpace(parts[0])
	}
	return r.Host
}

// IsHTTPS reports whether the request is HTTPS, checking TLS or X-Forwarded-Proto.
func IsHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	return strings.EqualFold(proto, "https")
}

// RequestScheme returns "https" when the request is HTTPS and "http" otherwise.
func RequestScheme(r *http.Request) string {
	if IsHTTPS(r) {
		return "https"
	}
	return "http"
}

// DetectBaseURL builds a base URL (scheme://host) using request headers with a fallback portal URL.
func DetectBaseURL(r *http.Request, fallbackPortalURL string) string {
	scheme := RequestScheme(r)
	host := ForwardedHost(r)
	if host == "" && fallbackPortalURL != "" {
		if u, err := url.Parse(fallbackPortalURL); err == nil {
			host = u.Host
			if u.Scheme != "" {
				scheme = u.Scheme
			}
		}
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

// DetectRelayURL builds a relay WebSocket URL (ws[s]://host/relay) using request headers with a fallback portal URL.
func DetectRelayURL(r *http.Request, fallbackPortalURL string) string {
	wsScheme := "ws"
	if IsHTTPS(r) {
		wsScheme = "wss"
	}
	host := ForwardedHost(r)
	if host == "" && fallbackPortalURL != "" {
		if u, err := url.Parse(fallbackPortalURL); err == nil {
			host = u.Host
			if u.Scheme == "https" {
				wsScheme = "wss"
			}
		}
	}
	return fmt.Sprintf("%s://%s/relay", wsScheme, host)
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

// DefaultBootstrapFrom derives a websocket bootstrap URL from a base portal URL or host.
// It prefers NormalizePortalURL for consistent mapping and falls back to localhost.
// Examples:
//   - "https://portal.example.com" -> "wss://portal.example.com/relay"
//   - "http://portal.example.com"  -> "ws://portal.example.com/relay"
//   - "localhost:4017"             -> "wss://localhost:4017/relay"
//   - ""                           -> "ws://localhost:4017/relay"
func DefaultBootstrapFrom(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return "ws://localhost:4017/relay"
	}
	if u, err := NormalizePortalURL(base); err == nil && u != "" {
		return u
	}
	host := StripScheme(strings.TrimSuffix(base, "/"))
	if host == "" {
		return "ws://localhost:4017/relay"
	}
	return "ws://" + host + "/relay"
}
