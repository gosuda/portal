package wsstream

import (
	"io"

	"github.com/gorilla/websocket"
)

type WsStream struct {
	Conn          *websocket.Conn
	currentReader io.Reader
}

func (g *WsStream) Read(p []byte) (n int, err error) {
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

	return n, err
}

func (g *WsStream) Write(p []byte) (n int, err error) {
	err = g.Conn.WriteMessage(websocket.BinaryMessage, p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (g *WsStream) Close() error {
	return g.Conn.Close()
}
