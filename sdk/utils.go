package sdk

import (
	"context"
	"fmt"
	"io"
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

// newWebSocketDialer returns a dialer that establishes WebSocket connections
// and wraps them as io.ReadWriteCloser.
func newWebSocketDialer() func(context.Context, string) (io.ReadWriteCloser, error) {
	return func(ctx context.Context, url string) (io.ReadWriteCloser, error) {
		wsConn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			return nil, err
		}
		return &wsstream.WsStream{Conn: wsConn}, nil
	}
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

// normalizeBootstrapServer takes various user-friendly server inputs and
// converts them into a proper WebSocket URL.
// Examples:
//   - "wss://localhost:4017/relay" -> unchanged
//   - "ws://localhost:4017/relay"  -> unchanged
//   - "http://example.com"        -> "ws://example.com/relay"
//   - "https://example.com"       -> "wss://example.com/relay"
//   - "localhost:4017"            -> "wss://localhost:4017/relay"
//   - "example.com"               -> "wss://example.com/relay"
func normalizeBootstrapServer(raw string) (string, error) {
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
