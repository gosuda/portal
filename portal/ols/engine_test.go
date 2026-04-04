package ols

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// mockPeerDialer implements PeerDialer for tests.
type mockPeerDialer struct {
	peers   map[string]string
	dialed  []string
	conn    net.Conn
	dialCh  chan struct{}
}

func newMockPeerDialer(conn net.Conn, peers map[string]string) *mockPeerDialer {
	return &mockPeerDialer{
		conn:   conn,
		peers:  peers,
		dialCh: make(chan struct{}, 1),
	}
}

func (m *mockPeerDialer) PeerAddr(nodeID string) (string, bool) {
	addr, ok := m.peers[nodeID]
	return addr, ok
}

func (m *mockPeerDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	m.dialed = append(m.dialed, address)
	select {
	case m.dialCh <- struct{}{}:
	default:
	}
	return m.conn, nil
}

type wrappedConn struct {
	net.Conn
	remote net.Addr
}

func (c *wrappedConn) RemoteAddr() net.Addr {
	return c.remote
}

type mockAddr string

func (a mockAddr) Network() string { return "mock" }
func (a mockAddr) String() string  { return string(a) }

func TestEngineRouteConnForwardsForOLSTarget(t *testing.T) {
	engine := New("node0")
	// Configure the grid deterministically: node0=self at (0,0), node1 target at (0,1).
	engine.manager.UpdateNodes([]string{"node0", "node1", "node2", "node3"})

	clientEnd, serverEnd := net.Pipe()
	proxyLocal, proxyRemote := net.Pipe()
	defer proxyRemote.Close()
	defer serverEnd.Close()

	dialer := newMockPeerDialer(proxyLocal, map[string]string{
		"node1": "overlay-node1:7778",
	})

	// Remote address string "b" hashes to row=0; server name "a" hashes to col=1.
	wrapped := &wrappedConn{
		Conn:   clientEnd,
		remote: mockAddr("b"),
	}

	done := make(chan bool, 1)
	go func() {
		done <- engine.RouteConn(context.Background(), wrapped, "a", dialer)
	}()

	select {
	case <-dialer.dialCh:
	case <-time.After(2 * time.Second):
		t.Fatal("DialContext was not invoked")
	}

	// Drain the route context header so subsequent payload reads see only proxied data.
	header := make([]byte, 8)
	if _, err := io.ReadFull(proxyRemote, header); err != nil {
		t.Fatalf("read route header: %v", err)
	}
	length := binary.BigEndian.Uint32(header[4:])
	ctxPayload := make([]byte, length)
	if _, err := io.ReadFull(proxyRemote, ctxPayload); err != nil {
		t.Fatalf("read route payload: %v", err)
	}

	// Verify that bytes sent through the client connection are proxied to the remote.
	const payload = "hello-via-ols"
	if _, err := serverEnd.Write([]byte(payload)); err != nil {
		t.Fatalf("write client payload: %v", err)
	}

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(proxyRemote, buf); err != nil {
		t.Fatalf("read proxied payload: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("proxied payload = %q, want %q", buf, payload)
	}

	_ = serverEnd.Close()
	_ = proxyRemote.Close()

	select {
	case forwarded := <-done:
		if !forwarded {
			t.Fatal("RouteConn returned false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RouteConn did not return")
	}

	if len(dialer.dialed) != 1 || dialer.dialed[0] != "overlay-node1:7778" {
		t.Fatalf("dialer.dialed = %v, want [overlay-node1:7778]", dialer.dialed)
	}
}
