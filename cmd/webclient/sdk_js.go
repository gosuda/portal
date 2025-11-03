package main

import (
	"context"
	"io"

	"gosuda.org/portal/cmd/webclient/wsjs"
)

// WebSocketDialerJS creates a WebSocket dialer function for JavaScript/WebAssembly environment
func WebSocketDialerJS() func(context.Context, string) (io.ReadWriteCloser, error) {
	return func(ctx context.Context, url string) (io.ReadWriteCloser, error) {
		// Use the wsjs package to create a WebSocket connection
		conn, err := wsjs.Dial(url)
		if err != nil {
			return nil, err
		}

		// Wrap the WebSocket connection with WsStream for io.ReadWriteCloser interface
		return wsjs.NewWsStream(conn), nil
	}
}
