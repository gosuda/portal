//go:build tinygo

package utils

import (
	"context"
	"errors"
	"io"
	"net/http"
)

// NewWebSocketDialer returns a no-op dialer for TinyGo builds.
// The actual dialer should be injected via sdk.WithDialer.
func NewWebSocketDialer() func(context.Context, string) (io.ReadWriteCloser, error) {
	return func(ctx context.Context, url string) (io.ReadWriteCloser, error) {
		return nil, errors.New("default websocket dialer not supported in tinygo; use sdk.WithDialer")
	}
}

// Stubs for functions not used in TinyGo client but required if referenced

func UpgradeWebSocket(w http.ResponseWriter, r *http.Request, responseHeader http.Header) (interface{}, error) {
	return nil, errors.New("not supported")
}

func UpgradeToWSStream(w http.ResponseWriter, r *http.Request, responseHeader http.Header) (io.ReadWriteCloser, interface{}, error) {
	return nil, nil, errors.New("not supported")
}

func IsLocalhost(r *http.Request) bool {
	return false
}
