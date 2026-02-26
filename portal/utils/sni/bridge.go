package sni

import (
	"io"

	"gosuda.org/portal/portal/utils/pool"
)

// CopyFunc is a function that copies data from src to dst, returning
// the number of bytes written and any error encountered.
type CopyFunc func(dst io.Writer, src io.Reader) (int64, error)

// BridgeConnections relays data bidirectionally between two connections
// until either side closes or errors. Both connections are closed when
// the bridge completes.
func BridgeConnections(a, b io.ReadWriteCloser) {
	errCh := make(chan error, 2)

	go func() {
		buf := *pool.Buffer64K.Get().(*[]byte)
		defer pool.Buffer64K.Put(&buf)
		_, err := io.CopyBuffer(b, a, buf)
		errCh <- err
		_ = b.Close()
	}()

	go func() {
		buf := *pool.Buffer64K.Get().(*[]byte)
		defer pool.Buffer64K.Put(&buf)
		_, err := io.CopyBuffer(a, b, buf)
		errCh <- err
		_ = a.Close()
	}()

	// Wait for first direction to complete, then cleanup.
	<-errCh
	_ = a.Close()
	_ = b.Close()
	<-errCh
}

// BridgeConnectionsWithCopy relays data bidirectionally using the provided
// copy function. If copyFn is nil, falls back to BridgeConnections.
// Both connections are closed when the bridge completes.
func BridgeConnectionsWithCopy(a, b io.ReadWriteCloser, copyFn CopyFunc) {
	if copyFn == nil {
		BridgeConnections(a, b)
		return
	}

	errCh := make(chan error, 2)

	go func() {
		_, err := copyFn(b, a)
		errCh <- err
		_ = b.Close()
	}()

	go func() {
		_, err := copyFn(a, b)
		errCh <- err
		_ = a.Close()
	}()

	<-errCh
	_ = a.Close()
	_ = b.Close()
	<-errCh
}
