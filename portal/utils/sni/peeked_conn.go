package sni

import (
	"io"
	"net"
)

// peekedConn wraps a net.Conn with a prepended reader so that bytes
// consumed during SNI peeking are replayed to downstream readers.
// All methods except Read delegate to the underlying Conn.
type peekedConn struct {
	net.Conn
	reader io.Reader
}

// NewPeekedConn creates a net.Conn wrapper that prepends peekedReader
// before the underlying connection's data stream.
func NewPeekedConn(conn net.Conn, peekedReader io.Reader) net.Conn {
	return &peekedConn{
		Conn:   conn,
		reader: peekedReader,
	}
}

// Read reads from the prepended reader first, then from the underlying connection.
func (c *peekedConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}
