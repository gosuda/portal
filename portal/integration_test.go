package portal

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdverb"
	"gosuda.org/portal/utils"
)

// generateTestCredential creates a new credential for testing
func generateTestCredential(t *testing.T) *cryptoops.Credential {
	cred, err := cryptoops.NewCredential()
	require.NoError(t, err)
	return cred
}

func TestIntegration_FullFlow(t *testing.T) {
	// 1. Setup Relay Server
	serverCred := generateTestCredential(t)
	server := NewRelayServer(serverCred, []string{"localhost:8080"})
	server.Start()
	defer server.Stop()

	// Create a listener for the server with TCP_NODELAY enabled
	rawListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	listener := utils.NewTCPNoDelayListener(rawListener)
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.HandleConnection(conn)
		}
	}()

	serverAddr := listener.Addr().String()

	// 2. Setup Host Client (Service Provider)
	hostCred := generateTestCredential(t)
	hostConn, err := net.Dial("tcp", serverAddr)
	require.NoError(t, err)
	require.NoError(t, utils.SetTCPNoDelay(hostConn))

	hostClient := NewRelayClient(hostConn)
	require.NotNil(t, hostClient)
	defer hostClient.Close()

	// Register Lease
	lease := &rdverb.Lease{
		Name: "test-service",
		Alpn: []string{"test-proto"},
	}
	err = hostClient.RegisterLease(hostCred, lease)
	require.NoError(t, err)

	// Handle incoming connections on Host
	go func() {
		for conn := range hostClient.IncomingConnection() {
			go func(c *IncomingConn) {
				defer c.Close()
				// Echo server
				io.Copy(c, c)
			}(conn)
		}
	}()

	// 3. Setup Peer Client (Consumer)
	peerCred := generateTestCredential(t)
	peerConn, err := net.Dial("tcp", serverAddr)
	require.NoError(t, err)
	require.NoError(t, utils.SetTCPNoDelay(peerConn))

	peerClient := NewRelayClient(peerConn)
	require.NotNil(t, peerClient)
	defer peerClient.Close()

	// 4. Peer connects to Host
	code, conn, err := peerClient.RequestConnection(hostCred.ID(), "test-proto", peerCred)
	require.NoError(t, err)
	assert.Equal(t, rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED, code)
	require.NotNil(t, conn)
	defer conn.Close()

	// 5. Verify Data Transfer
	message := []byte("Hello, Portal!")
	_, err = conn.Write(message)
	require.NoError(t, err)

	buffer := make([]byte, len(message))
	_, err = io.ReadFull(conn, buffer)
	require.NoError(t, err)
	assert.Equal(t, message, buffer)

	// 6. Verify Lease Cleanup
	err = hostClient.DeregisterLease(hostCred)
	require.NoError(t, err)

	// Wait a bit for propagation
	time.Sleep(100 * time.Millisecond)

	// Connection should fail now
	code, _, err = peerClient.RequestConnection(hostCred.ID(), "test-proto", peerCred)
	assert.Equal(t, rdverb.ResponseCode_RESPONSE_CODE_INVALID_IDENTITY, code)
}
