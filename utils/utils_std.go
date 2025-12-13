//go:build !js || !wasm

package utils

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"gosuda.org/portal/portal/utils/wsstream"
)

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

func IsLocalhost(r *http.Request) bool {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}

	// If a proxy/adapter reports a hostname, allow Docker Desktop host alias.
	if strings.EqualFold(host, "host.docker.internal") {
		return true
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// Try resolving hostnames to IPs (best-effort).
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
