//go:build js && wasm

package utils

import (
	"context"
	"io"
)

// NewWebSocketDialer creates a new WebSocket dialer for TinyGo/WASM
func NewWebSocketDialer() func(context.Context, string) (io.ReadWriteCloser, error) {
	return func(ctx context.Context, url string) (io.ReadWriteCloser, error) {
		conn, err := DialWebSocket(url)
		if err != nil {
			return nil, err
		}
		return NewWsStream(conn), nil
	}
}

// Stubs for functions not used in TinyGo client but required if referenced

func IsLocalhost(host string) bool {
	return false
}
