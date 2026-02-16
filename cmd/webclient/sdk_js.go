package main

import (
	"context"

	"gosuda.org/portal/cmd/webclient/wtjs"
	"gosuda.org/portal/portal"
)

// WebTransportDialerJS creates a WebTransport dialer for the browser WASM environment.
// certHashes provides SHA-256 certificate hashes for self-signed dev certs.
func WebTransportDialerJS(certHashes [][]byte) func(context.Context, string) (portal.Session, error) {
	return func(ctx context.Context, url string) (portal.Session, error) {
		return wtjs.Dial(url, certHashes)
	}
}
