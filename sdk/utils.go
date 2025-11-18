package sdk

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/utils/wsstream"
)

func NewCredential() *cryptoops.Credential {
	cred, err := cryptoops.NewCredential()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create credential")
	}
	return cred
}

// NewWebSocketDialer returns a dialer that establishes WebSocket connections
// and wraps them as io.ReadWriteCloser.
func NewWebSocketDialer() func(context.Context, string) (io.ReadWriteCloser, error) {
	return func(ctx context.Context, url string) (io.ReadWriteCloser, error) {
		wsConn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			return nil, err
		}
		return &wsstream.WsStream{Conn: wsConn}, nil
	}
}

// DefaultWebSocketUpgrader provides a permissive upgrader used across cmd binaries
var DefaultWebSocketUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// UpgradeWebSocket upgrades the request/response to a WebSocket connection using DefaultWebSocketUpgrader
func UpgradeWebSocket(w http.ResponseWriter, r *http.Request, responseHeader http.Header) (*websocket.Conn, error) {
	return DefaultWebSocketUpgrader.Upgrade(w, r, responseHeader)
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

// isURLSafeName checks if a name contains only URL-safe characters.
// Disallows: spaces, special characters like /, ?, &, =, %, etc.
// Note: Browsers will automatically URL-encode non-ASCII characters.
func isURLSafeName(name string) bool {
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
