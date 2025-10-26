package relaydns

import (
	"io"

	"github.com/gorilla/websocket"
)

type wsStream struct {
	c             *websocket.Conn
	currentReader io.Reader
}

func (g *wsStream) Read(p []byte) (n int, err error) {
	if g.currentReader == nil {
		_, reader, err := g.c.NextReader()
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

func (g *wsStream) Write(p []byte) (n int, err error) {
	err = g.c.WriteMessage(websocket.BinaryMessage, p)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (g *wsStream) Close() error {
	return g.c.Close()
}
