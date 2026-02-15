package wsstream

import (
	"io"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

// webSocketConn defines the interface for WebSocket connections.
// This allows for mocking in tests while using the real websocket.Conn in production.
type webSocketConn interface {
	NextReader() (int, io.Reader, error)
	WriteMessage(int, []byte) error
	Close() error
}

// WsStream wraps a WebSocket connection to implement io.Reader and io.Writer.
type WsStream struct {
	Conn          webSocketConn
	currentReader io.Reader
	writeMu       sync.Mutex
	readMu        sync.Mutex
}

// New creates a new WsStream from a gorilla/websocket connection.
func New(conn *websocket.Conn) *WsStream {
	return &WsStream{
		Conn: conn,
	}
}

func (g *WsStream) Read(p []byte) (n int, err error) {
	g.readMu.Lock()
	defer g.readMu.Unlock()

	// Handle empty buffer - standard io.Reader behavior
	if len(p) == 0 {
		return 0, nil
	}

	for {
		// Get a reader if we don't have one
		if g.currentReader == nil {
			_, reader, err := g.Conn.NextReader()
			if err != nil {
				// Convert websocket close errors to io.EOF
				if err != nil && strings.HasPrefix(err.Error(), "websocket: close ") {
					return 0, io.EOF
				}
				return 0, err
			}
			g.currentReader = reader
		}

		n, err = g.currentReader.Read(p)
		if err == io.EOF {
			// Current message exhausted, try to get next one
			g.currentReader = nil
			continue
		}

		if err != nil && strings.HasPrefix(err.Error(), "websocket: close ") {
			return 0, io.EOF
		}

		return n, err
	}
}

func (g *WsStream) Write(p []byte) (n int, err error) {
	g.writeMu.Lock()
	defer g.writeMu.Unlock()
	err = g.Conn.WriteMessage(websocket.BinaryMessage, p)
	if err != nil {
		if strings.HasPrefix(err.Error(), "websocket: close ") {
			return 0, io.EOF
		}
		return 0, err
	}

	return len(p), nil
}

func (g *WsStream) Close() error {
	return g.Conn.Close()
}
