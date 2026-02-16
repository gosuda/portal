package utils

import (
	"context"
	"io"
	"net/http"

	"github.com/gorilla/websocket"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/utils/wsstream"
)

// NewWebSocketDialer returns a dialer that establishes WebSocket connections
// and wraps them in a yamux Session.
func NewWebSocketDialer() func(context.Context, string) (portal.Session, error) {
	return func(ctx context.Context, url string) (portal.Session, error) {
		wsConn, resp, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			if resp != nil {
				resp.Body.Close()
			}
			return nil, err
		}
		// Response body is closed by the Dialer on successful connection
		stream := wsstream.New(wsConn)
		sess, err := portal.NewYamuxClientSession(stream)
		if err != nil {
			stream.Close()
			return nil, err
		}
		return sess, nil
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
	return wsstream.New(wsConn), wsConn, nil
}
