package portal

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdverb"
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

	// Create connected session pair (replaces TCP + yamux)
	clientSess, serverSess := NewPipeSessionPair()
	server.HandleSession(serverSess)

	// 2. Setup Host Client (Service Provider)
	hostCred := generateTestCredential(t)

	hostClient := NewRelayClient(clientSess)
	require.NotNil(t, hostClient)
	defer hostClient.Close()

	// Register Lease
	lease := &rdverb.Lease{
		Name: "test-service",
		Alpn: []string{"test-proto"},
	}
	err := hostClient.RegisterLease(hostCred, lease)
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

	peerClientSess, peerServerSess := NewPipeSessionPair()
	server.HandleSession(peerServerSess)

	peerClient := NewRelayClient(peerClientSess)
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
