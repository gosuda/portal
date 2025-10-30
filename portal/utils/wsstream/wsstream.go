package wsstream

import (
	"io"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

type WsStream struct {
	Conn          *websocket.Conn
	currentReader io.Reader
	writeMu       sync.Mutex
	readMu        sync.Mutex
}

func (g *WsStream) Read(p []byte) (n int, err error) {
	g.readMu.Lock()
	defer g.readMu.Unlock()
	if g.currentReader == nil {
		_, reader, err := g.Conn.NextReader()
		if err != nil {
			return 0, err
		}
		g.currentReader = reader
	}

	n, err = g.currentReader.Read(p)
	if err == io.EOF {
		g.currentReader = nil
		err = nil
	}

	if err != nil && strings.HasPrefix(err.Error(), "websocket: close ") {
		return 0, io.EOF
	}

	return n, err
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
