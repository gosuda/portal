
package portal

import (
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdverb"
)

// setupBenchmarkEnv sets up a complete relay environment for benchmarking.
// It creates a relay server, a host client that registers a lease,
// and an echo server on the host side.
// It returns the server address, the host's public ID, and a cleanup function.
func setupBenchmarkEnv(b *testing.B) (serverAddr string, hostID string, cleanup func()) {
	b.Helper()

	// 1. Setup Relay Server
	serverCred, err := cryptoops.NewCredential()
	require.NoError(b, err)
	server := NewRelayServer(serverCred, []string{"localhost:8080"})
	server.Start()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(b, err)

	serverAddr = listener.Addr().String()

	serverErrCh := make(chan error, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				serverErrCh <- err
				return
			}
			go server.HandleConnection(conn)
		}
	}()

	// 2. Setup Host Client (Service Provider)
	hostCred, err := cryptoops.NewCredential()
	require.NoError(b, err)
	hostConn, err := net.Dial("tcp", serverAddr)
	require.NoError(b, err)

	hostClient := NewRelayClient(hostConn)
	require.NotNil(b, hostClient)

	lease := &rdverb.Lease{
		Name: "benchmark-service",
		Alpn: []string{"bench-proto"},
	}
	err = hostClient.RegisterLease(hostCred, lease)
	require.NoError(b, err)

	// Handle incoming connections on Host (echo server)
	go func() {
		for conn := range hostClient.IncomingConnection() {
			go func(c *IncomingConn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()

	cleanup = func() {
		hostClient.DeregisterLease(hostCred)
		hostClient.Close()
		server.Stop()
		listener.Close()
	}

	return serverAddr, hostCred.ID(), cleanup
}

// BenchmarkFullHandshakeAndTransfer measures the entire process of a peer
// connecting, establishing a session with a host, and transferring a small
// payload. This is a good measure of connection latency.
func BenchmarkFullHandshakeAndTransfer(b *testing.B) {
	serverAddr, hostID, cleanup := setupBenchmarkEnv(b)
	defer cleanup()

	peerCred, err := cryptoops.NewCredential()
	require.NoError(b, err)

	payload := []byte("hello benchmark")
	readBuffer := make([]byte, len(payload))

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		peerConn, err := net.Dial("tcp", serverAddr)
		if err != nil {
			b.Fatalf("Peer failed to connect to server: %v", err)
		}

		peerClient := NewRelayClient(peerConn)
		if peerClient == nil {
			b.Fatalf("Failed to create peer client")
		}

		_, conn, err := peerClient.RequestConnection(hostID, "bench-proto", peerCred)
		if err != nil {
			b.Fatalf("Peer failed to request connection: %v", err)
		}
		if conn == nil {
			b.Fatalf("Returned connection is nil")
		}

		// Data transfer
		_, err = conn.Write(payload)
		if err != nil {
			b.Fatalf("Failed to write payload: %v", err)
		}

		_, err = io.ReadFull(conn, readBuffer)
		if err != nil {
			b.Fatalf("Failed to read payload: %v", err)
		}

		conn.Close()
		peerClient.Close()
	}
}

// BenchmarkHighVolumeTransfer measures the throughput of the relay connection
// by transferring a larger payload over a pre-established connection.
func BenchmarkHighVolumeTransfer(b *testing.B) {
	serverAddr, hostID, cleanup := setupBenchmarkEnv(b)
	defer cleanup()

	peerCred, err := cryptoops.NewCredential()
	require.NoError(b, err)

	// Setup a single, persistent peer connection for the benchmark
	peerConn, err := net.Dial("tcp", serverAddr)
	require.NoError(b, err)
	peerClient := NewRelayClient(peerConn)
	require.NotNil(b, peerClient)
	defer peerClient.Close()

	_, conn, err := peerClient.RequestConnection(hostID, "bench-proto", peerCred)
	require.NoError(b, err)
	require.NotNil(b, conn)
	defer conn.Close()

	// 64KB payload
	const payloadSize = 64 * 1024
	payload := make([]byte, payloadSize)
	readBuffer := make([]byte, payloadSize)
	b.SetBytes(payloadSize)
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := conn.Write(payload)
		if err != nil {
			b.Fatalf("Write failed: %v", err)
		}

		_, err = io.ReadFull(conn, readBuffer)
		if err != nil {
			b.Fatalf("Read failed: %v", err)
		}
	}
}
