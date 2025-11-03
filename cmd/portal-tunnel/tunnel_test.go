package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gosuda.org/portal/sdk"
)

// TestTunnelEndToEnd tests the complete tunnel functionality
func TestTunnelEndToEnd(t *testing.T) {
	// Start a local HTTP server
	localPort := findFreePort(t)
	localAddr := fmt.Sprintf("localhost:%d", localPort)
	
	localServer := &http.Server{
		Addr: localAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("Hello from local server!"))
		}),
	}
	
	go func() {
		localServer.ListenAndServe()
	}()
	defer localServer.Close()
	
	// Wait for local server to start
	time.Sleep(100 * time.Millisecond)
	
	// Verify local server is reachable
	resp, err := http.Get(fmt.Sprintf("http://%s", localAddr))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
	
	t.Logf("✓ Local server started on %s", localAddr)
	
	// Create relay server (mock or use actual relay server)
	// For this test, we'll assume a relay server is running on localhost:4017
	relayURL := "ws://localhost:4017/relay"
	
	// Create credential for tunnel
	cred := sdk.NewCredential()
	leaseID := cred.ID()
	serviceName := fmt.Sprintf("test-tunnel-%d", time.Now().Unix())
	
	t.Logf("Creating tunnel with:")
	t.Logf("  Service name: %s", serviceName)
	t.Logf("  Lease ID: %s", leaseID)
	
	// Create SDK client
	client, err := sdk.NewClient(func(c *sdk.RDClientConfig) {
		c.BootstrapServers = []string{relayURL}
	})
	if err != nil {
		t.Skipf("Relay server not available: %v", err)
		return
	}
	defer client.Close()
	
	// Register listener (server side of tunnel)
	listener, err := client.Listen(cred, serviceName, []string{"http/1.1"})
	require.NoError(t, err)
	defer listener.Close()
	
	t.Logf("✓ Tunnel registered successfully")
	
	// Start proxy goroutine (simulate portal-tunnel behavior)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	proxyErrors := make(chan error, 10)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			
			relayConn, err := listener.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					proxyErrors <- fmt.Errorf("accept error: %w", err)
					continue
				}
			}
			
			// Proxy connection to local server
			go func(relayConn net.Conn) {
				defer relayConn.Close()
				
				localConn, err := net.Dial("tcp", localAddr)
				if err != nil {
					proxyErrors <- fmt.Errorf("local dial error: %w", err)
					return
				}
				defer localConn.Close()
				
				// Bidirectional copy
				errCh := make(chan error, 2)
				go func() {
					_, err := io.Copy(localConn, relayConn)
					errCh <- err
				}()
				go func() {
					_, err := io.Copy(relayConn, localConn)
					errCh <- err
				}()
				
				<-errCh
				localConn.Close()
				relayConn.Close()
				<-errCh
			}(relayConn)
		}
	}()
	
	// Wait for tunnel to be ready
	time.Sleep(500 * time.Millisecond)
	
	t.Logf("✓ Tunnel proxy started")
	
	// Create a client that will connect through the tunnel
	clientCred := sdk.NewCredential()
	clientSDK, err := sdk.NewClient(func(c *sdk.RDClientConfig) {
		c.BootstrapServers = []string{relayURL}
	})
	require.NoError(t, err)
	defer clientSDK.Close()
	
	// Connect through the tunnel
	tunnelConn, err := clientSDK.Dial(clientCred, leaseID, "http/1.1")
	require.NoError(t, err)
	defer tunnelConn.Close()
	
	t.Logf("✓ Connected through tunnel")
	
	// Send HTTP request through tunnel
	req := "GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n"
	_, err = tunnelConn.Write([]byte(req))
	require.NoError(t, err)
	
	// Read response
	response := make([]byte, 4096)
	n, err := tunnelConn.Read(response)
	require.NoError(t, err)
	
	responseStr := string(response[:n])
	t.Logf("Received response (%d bytes):\n%s", n, responseStr)
	
	// Verify response
	assert.Contains(t, responseStr, "HTTP/1.1 200 OK")
	assert.Contains(t, responseStr, "Hello from local server!")
	
	t.Logf("✓ Tunnel test completed successfully!")
	
	// Check for proxy errors
	select {
	case err := <-proxyErrors:
		t.Logf("Proxy error (non-fatal): %v", err)
	default:
		// No errors
	}
}

// TestProxyConnection tests the proxy connection logic
func TestProxyConnection(t *testing.T) {
	// Create a mock local server
	localPort := findFreePort(t)
	localAddr := fmt.Sprintf("localhost:%d", localPort)
	
	localListener, err := net.Listen("tcp", localAddr)
	require.NoError(t, err)
	defer localListener.Close()
	
	// Local server that echoes back
	go func() {
		for {
			conn, err := localListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c) // Echo back
			}(conn)
		}
	}()
	
	// Create mock relay connection (using pipes)
	relayConn, clientConn := net.Pipe()
	defer relayConn.Close()
	defer clientConn.Close()
	
	// Start proxy
	go func() {
		err := proxyConnection(relayConn, localAddr, 1)
		if err != nil && !strings.Contains(err.Error(), "closed") {
			t.Logf("Proxy error: %v", err)
		}
	}()
	
	// Send data through client side
	testData := "Hello, tunnel!"
	_, err = clientConn.Write([]byte(testData))
	require.NoError(t, err)
	
	// Read echoed data
	buf := make([]byte, len(testData))
	n, err := clientConn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, testData, string(buf[:n]))
	
	t.Logf("✓ Proxy connection test passed")
}

// TestLocalServiceConnectivity tests the local service connectivity check
func TestLocalServiceConnectivity(t *testing.T) {
	// Start a local server
	localPort := findFreePort(t)
	localAddr := fmt.Sprintf("localhost:%d", localPort)
	
	listener, err := net.Listen("tcp", localAddr)
	require.NoError(t, err)
	defer listener.Close()
	
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	
	// Test connectivity (simulating the check in runExpose)
	testConn, err := net.Dial("tcp", localAddr)
	require.NoError(t, err)
	testConn.Close()
	
	t.Logf("✓ Local service connectivity test passed")
}

// TestLocalServiceNotReachable tests error handling when local service is not available
func TestLocalServiceNotReachable(t *testing.T) {
	// Try to connect to a port that's not listening
	localAddr := "localhost:19999"
	
	_, err := net.Dial("tcp", localAddr)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")
	
	t.Logf("✓ Error handling test passed")
}

// TestPadRight tests the padding utility function
func TestPadRight(t *testing.T) {
	tests := []struct {
		input    string
		length   int
		expected int
	}{
		{"test", 10, 10},
		{"hello", 5, 5},
		{"toolong", 3, 7}, // Should not truncate
	}
	
	for _, tt := range tests {
		result := padRight(tt.input, tt.length)
		assert.Equal(t, tt.expected, len(result))
		assert.True(t, strings.HasPrefix(result, tt.input))
	}
}

// TestExtractHost tests the host extraction from WebSocket URL
func TestExtractHost(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ws://localhost:4017/relay", "localhost:4017"},
		{"wss://example.com:443/path", "example.com:443"},
		{"ws://192.168.1.1:8080/test", "192.168.1.1:8080"},
		{"ws://example.com/", "example.com"},
	}
	
	for _, tt := range tests {
		result := extractHost(tt.input)
		assert.Equal(t, tt.expected, result, "Failed for input: %s", tt.input)
	}
}

// Helper function to find a free port for testing
func findFreePort(t *testing.T) int {
	listener, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}
