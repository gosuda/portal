package utils

import (
	"context"
	"io"
	"net/http"

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
