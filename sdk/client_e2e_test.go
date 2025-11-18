package sdk

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/core/cryptoops"
)

func init() {
	// Set zerolog to Debug level for testing
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
}

// TestE2E_ClientToAppThroughRelay tests the full end-to-end flow:
// SDK Client -> Relay Server -> Demo App
func TestE2E_ClientToAppThroughRelay(t *testing.T) {
	log.Info().Msg("=== Starting E2E Test ===")

	// 1. Create relay server credential
	log.Info().Msg("[TEST] Step 1: Creating relay server credential")
	relayServerCred, err := cryptoops.NewCredential()
	require.NoError(t, err, "Failed to create relay server credential")
	log.Debug().Str("relay_id", relayServerCred.ID()).Msg("[TEST] Relay server credential created")

	// 2. Start relay server
	log.Info().Msg("[TEST] Step 2: Starting relay server")
	relayServer := portal.NewRelayServer(relayServerCred, []string{"ws://127.0.0.1:14017/relay"})
	relayServer.Start()
	defer relayServer.Stop()

	// Start WebSocket server for relay
	relayAddr := "127.0.0.1:14017"
	relayMux := http.NewServeMux()
	relayMux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		log.Debug().Str("remote", r.RemoteAddr).Msg("[TEST] Relay server accepting WebSocket connection")
		stream, _, err := UpgradeToWSStream(w, r, nil)
		if err != nil {
			log.Error().Err(err).Msg("[TEST] Failed to upgrade WebSocket")
			return
		}
		if err := relayServer.HandleConnection(stream); err != nil {
			log.Error().Err(err).Msg("[TEST] Relay server error handling connection")
		}
	})

	relayHTTPServer := &http.Server{
		Addr:    relayAddr,
		Handler: relayMux,
	}

	go func() {
		log.Info().Str("addr", relayAddr).Msg("[TEST] Relay HTTP server starting")
		if err := relayHTTPServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("[TEST] Relay HTTP server error")
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		relayHTTPServer.Shutdown(ctx)
	}()

	// Wait for relay server to start
	time.Sleep(500 * time.Millisecond)
	log.Info().Msg("[TEST] Relay server started")

	// 3. Create app credential and SDK client
	log.Info().Msg("[TEST] Step 3: Creating app (listener) credential")
	appCred := NewCredential()
	log.Debug().Str("app_id", appCred.ID()).Msg("[TEST] App credential created")

	// 4. Create app SDK client and register listener
	log.Info().Msg("[TEST] Step 4: Creating app SDK client")
	appClient, err := NewClient(func(c *ClientConfig) {
		c.BootstrapServers = []string{"ws://127.0.0.1:14017/relay"}
	})
	require.NoError(t, err, "Failed to create app SDK client")
	defer appClient.Close()
	log.Info().Msg("[TEST] App SDK client created")

	// 5. Register listener on app side
	log.Info().Msg("[TEST] Step 5: Registering app listener")
	appListener, err := appClient.Listen(appCred, "test-app", []string{"http/1.1"})
	require.NoError(t, err, "Failed to create app listener")
	defer appListener.Close()
	log.Info().Str("lease_id", appCred.ID()).Msg("[TEST] App listener registered")

	// 6. Start serving HTTP on app listener
	log.Info().Msg("[TEST] Step 6: Starting HTTP server on app listener")
	appMux := http.NewServeMux()
	testMessage := "Hello from E2E Test App!"
	appMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Debug().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Str("remote", r.RemoteAddr).
			Msg("[TEST] App received HTTP request")
		fmt.Fprintf(w, "%s\n", testMessage)
		fmt.Fprintf(w, "Request from: %s\n", r.RemoteAddr)
	})

	appErrChan := make(chan error, 1)
	go func() {
		log.Info().Msg("[TEST] App HTTP server starting on listener")
		appErrChan <- http.Serve(appListener, appMux)
	}()

	// Wait for app to be ready
	time.Sleep(1 * time.Second)
	log.Info().Msg("[TEST] App is ready to accept connections")

	// 7. Create client credential
	log.Info().Msg("[TEST] Step 7: Creating client credential")
	clientCred := NewCredential()
	log.Debug().Str("client_id", clientCred.ID()).Msg("[TEST] Client credential created")

	// 8. Create client SDK client
	log.Info().Msg("[TEST] Step 8: Creating client SDK client")
	clientSDK, err := NewClient(func(c *ClientConfig) {
		c.BootstrapServers = []string{"ws://127.0.0.1:14017/relay"}
	})
	require.NoError(t, err, "Failed to create client SDK")
	defer clientSDK.Close()
	log.Info().Msg("[TEST] Client SDK client created")

	// 9. Wait for lease to be fully registered
	log.Info().Msg("[TEST] Step 9: Waiting for lease propagation")
	time.Sleep(2 * time.Second)

	// 10. Dial to app through relay
	log.Info().Msg("[TEST] Step 10: Client dialing to app through relay")
	conn, err := clientSDK.Dial(clientCred, appCred.ID(), "http/1.1")
	require.NoError(t, err, "Failed to dial to app")
	defer conn.Close()
	log.Info().
		Str("local", conn.LocalAddr().String()).
		Str("remote", conn.RemoteAddr().String()).
		Msg("[TEST] Connection established")

	// 11. Send HTTP request through the connection
	log.Info().Msg("[TEST] Step 11: Sending HTTP request through connection")

	// Create HTTP request
	req, err := http.NewRequest("GET", "http://test-app/", nil)
	require.NoError(t, err, "Failed to create HTTP request")

	// Write HTTP request to connection
	if err := req.Write(conn); err != nil {
		require.NoError(t, err, "Failed to write HTTP request")
	}
	log.Debug().Msg("[TEST] HTTP request sent")

	// Read HTTP response
	log.Info().Msg("[TEST] Step 12: Reading HTTP response")
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	require.NoError(t, err, "Failed to read HTTP response")
	defer resp.Body.Close()

	log.Debug().
		Int("status_code", resp.StatusCode).
		Str("status", resp.Status).
		Msg("[TEST] HTTP response received")

	// Read response body
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "Failed to read response body")

	responseStr := string(body)
	log.Info().Str("body", responseStr).Msg("[TEST] Response body received")

	// 12. Verify response
	log.Info().Msg("[TEST] Step 13: Verifying response")
	require.Equal(t, http.StatusOK, resp.StatusCode, "Expected status code 200")
	require.NotEmpty(t, body, "Expected non-empty response body")

	// Check if response contains test message
	bodyStr := string(body)
	require.NotEmpty(t, bodyStr, "Response body is empty")
	log.Info().Str("response", bodyStr).Msg("[TEST] Response verification successful")

	log.Info().Msg("=== E2E Test Completed Successfully ===")
}

// TestE2E_MultipleConnections tests multiple concurrent connections
func TestE2E_MultipleConnections(t *testing.T) {
	log.Info().Msg("=== Starting Multiple Connections Test ===")

	// Setup relay server
	relayServerCred, err := cryptoops.NewCredential()
	require.NoError(t, err, "Failed to create relay server credential")

	relayServer := portal.NewRelayServer(relayServerCred, []string{"ws://127.0.0.1:14018/relay"})
	relayServer.Start()
	defer relayServer.Stop()

	relayAddr := "127.0.0.1:14018"
	relayMux := http.NewServeMux()
	relayMux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		stream, _, err := UpgradeToWSStream(w, r, nil)
		if err != nil {
			return
		}
		relayServer.HandleConnection(stream)
	})

	relayHTTPServer := &http.Server{
		Addr:    relayAddr,
		Handler: relayMux,
	}

	go relayHTTPServer.ListenAndServe()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		relayHTTPServer.Shutdown(ctx)
	}()

	time.Sleep(500 * time.Millisecond)

	// Setup app
	appCred := NewCredential()

	appClient, err := NewClient(func(c *ClientConfig) {
		c.BootstrapServers = []string{"ws://127.0.0.1:14018/relay"}
	})
	require.NoError(t, err, "Failed to create app SDK client")
	defer appClient.Close()

	appListener, err := appClient.Listen(appCred, "multi-test-app", []string{"http/1.1"})
	require.NoError(t, err, "Failed to create app listener")
	defer appListener.Close()

	// Serve echo server
	go func() {
		for {
			conn, err := appListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c) // Echo back
			}(conn)
		}
	}()

	time.Sleep(1 * time.Second)

	// Create client
	clientCred := NewCredential()

	clientSDK, err := NewClient(func(c *ClientConfig) {
		c.BootstrapServers = []string{"ws://127.0.0.1:14018/relay"}
	})
	require.NoError(t, err, "Failed to create client SDK")
	defer clientSDK.Close()

	time.Sleep(2 * time.Second)

	// Test multiple concurrent connections
	numConnections := 5
	log.Info().Int("count", numConnections).Msg("[TEST] Testing multiple concurrent connections")

	for i := 0; i < numConnections; i++ {
		i := i
		go func() {
			log.Debug().Int("conn_num", i).Msg("[TEST] Starting connection")

			conn, err := clientSDK.Dial(clientCred, appCred.ID(), "http/1.1")
			if !assert.NoError(t, err, "Connection %d failed to dial", i) {
				return
			}
			defer conn.Close()

			testData := fmt.Sprintf("test-message-%d", i)

			// Write test data
			_, err = conn.Write([]byte(testData))
			if !assert.NoError(t, err, "Connection %d failed to write", i) {
				return
			}

			// Read echoed data
			buf := make([]byte, len(testData))
			_, err = io.ReadFull(conn, buf)
			if !assert.NoError(t, err, "Connection %d failed to read", i) {
				return
			}

			if !assert.Equal(t, testData, string(buf), "Connection %d: unexpected echo data", i) {
				return
			}

			log.Debug().Int("conn_num", i).Msg("[TEST] Connection successful")
		}()
	}

	time.Sleep(5 * time.Second)
	log.Info().Msg("=== Multiple Connections Test Completed ===")
}

// TestE2E_ConnectionTimeout tests timeout scenarios
func TestE2E_ConnectionTimeout(t *testing.T) {
	log.Info().Msg("=== Starting Connection Timeout Test ===")

	// Create client with non-existent relay
	clientCred := NewCredential()

	// This should fail or timeout appropriately
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := NewClient(func(c *ClientConfig) {
			c.BootstrapServers = []string{"ws://127.0.0.1:19999/relay"} // Non-existent
		})
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err, "Expected error when connecting to non-existent relay")
		log.Info().Err(err).Msg("[TEST] Got expected error")
	case <-ctx.Done():
		require.Fail(t, "Connection attempt did not complete within timeout")
	}

	// Try to dial to non-existent lease
	relayServerCred := NewCredential()

	relayServer := portal.NewRelayServer(relayServerCred, []string{"ws://127.0.0.1:14019/relay"})
	relayServer.Start()
	defer relayServer.Stop()

	relayAddr := "127.0.0.1:14019"
	relayMux := http.NewServeMux()
	relayMux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		stream, _, err := UpgradeToWSStream(w, r, nil)
		if err != nil {
			return
		}
		relayServer.HandleConnection(stream)
	})

	relayHTTPServer := &http.Server{
		Addr:    relayAddr,
		Handler: relayMux,
	}

	go relayHTTPServer.ListenAndServe()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		relayHTTPServer.Shutdown(ctx)
	}()

	time.Sleep(500 * time.Millisecond)

	clientSDK, err := NewClient(func(c *ClientConfig) {
		c.BootstrapServers = []string{"ws://127.0.0.1:14019/relay"}
	})
	require.NoError(t, err, "Failed to create client SDK")
	defer clientSDK.Close()

	time.Sleep(1 * time.Second)

	// Try to dial to non-existent lease
	log.Info().Msg("[TEST] Attempting to dial non-existent lease")
	_, err = clientSDK.Dial(clientCred, "non-existent-lease-id", "http/1.1")
	require.Error(t, err, "Expected error when dialing non-existent lease")
	log.Info().Err(err).Msg("[TEST] Got expected error for non-existent lease")

	log.Info().Msg("=== Connection Timeout Test Completed ===")
}
