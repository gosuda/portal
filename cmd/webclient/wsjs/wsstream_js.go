package wsjs

import (
	"sync"
)

// WsStream provides an io.Reader and io.Writer interface for WebSocket connections
type WsStream struct {
	conn          *Conn
	currentBuffer []byte
	readMu        sync.Mutex
	writeMu       sync.Mutex
}

// NewWsStream creates a new WsStream from a WebSocket connection
func NewWsStream(conn *Conn) *WsStream {
	return &WsStream{
		conn: conn,
	}
}

// Read implements io.Reader interface
func (ws *WsStream) Read(p []byte) (n int, err error) {
	ws.readMu.Lock()
	defer ws.readMu.Unlock()

	// If we have remaining data from previous message, use it first
	if len(ws.currentBuffer) > 0 {
		n = copy(p, ws.currentBuffer)
		ws.currentBuffer = ws.currentBuffer[n:]
		return n, nil
	}

	// Get next message from WebSocket
	msg, err := ws.conn.NextMessage()
	if err != nil {
		return 0, err
	}

	// Copy message data to buffer
	n = copy(p, msg)

	// Store any remaining data for next read
	if n < len(msg) {
		ws.currentBuffer = msg[n:]
	}

	return n, nil
}

// Write implements io.Writer interface
func (ws *WsStream) Write(p []byte) (n int, err error) {
	ws.writeMu.Lock()
	defer ws.writeMu.Unlock()

	err = ws.conn.Send(p)
	if err != nil {
		return 0, err
	}

	return len(p), nil
}

// Close closes the WebSocket connection
func (ws *WsStream) Close() error {
	return ws.conn.Close()
}
