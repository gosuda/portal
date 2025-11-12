package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"gosuda.org/portal/sdk"
)

var (
	flagRelayURL string
	flagHost     string
	flagPort     string
	flagName     string
)

func main() {
	if len(os.Args) < 2 {
		printTunnelUsage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "expose":
		fs := flag.NewFlagSet("expose", flag.ExitOnError)
		fs.StringVar(&flagRelayURL, "relay", "ws://localhost:4017/relay", "Portal relay server URL")
		fs.StringVar(&flagHost, "host", "localhost", "Local host to proxy to")
		fs.StringVar(&flagPort, "port", "4018", "Local port to proxy to")
		fs.StringVar(&flagName, "name", "", "Service name (will be generated if not provided)")
		_ = fs.Parse(os.Args[2:])

		if err := runExpose(); err != nil {
			log.Fatal().Err(err).Msg("Failed to expose")
		}
	case "-h", "--help", "help":
		printTunnelUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printTunnelUsage()
		os.Exit(2)
	}
}

func printTunnelUsage() {
	fmt.Println("portal-tunnel — Expose local services through Portal relay")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  portal-tunnel expose [port PORT] [--relay URL] [--name NAME] [--host HOST]")
}

func runExpose() error {
	localAddr := net.JoinHostPort(flagHost, flagPort)

	// Always wait until the local service is available
	log.Info().Msgf("Waiting for local service at %s (interval=%v)...", localAddr, time.Second)
	if err := waitForLocalService(localAddr, 0, time.Second); err != nil {
		return err
	}
	log.Info().Msgf("✓ Local service is reachable at %s", localAddr)

	// Create credential
	cred := sdk.NewCredential()
	leaseID := cred.ID()

	// Use provided name or generate from lease ID
	if flagName == "" {
		flagName = fmt.Sprintf("tunnel-%s", leaseID[:8])
	}

	log.Info().Msgf("Starting Portal Tunnel...")
	log.Info().Msgf("  Local:    %s", localAddr)
	log.Info().Msgf("  Relay:    %s", flagRelayURL)
	log.Info().Msgf("  Name:     %s", flagName)
	log.Info().Msgf("  Lease ID: %s", leaseID)

	// Create SDK client
	client, err := sdk.NewClient(func(c *sdk.RDClientConfig) {
		c.BootstrapServers = []string{flagRelayURL}
	})
	if err != nil {
		return fmt.Errorf("failed to connect to relay: %w", err)
	}
	defer client.Close()

	// Register listener
	listener, err := client.Listen(cred, flagName, []string{"http/1.1", "h2"}, nil)
	if err != nil {
		return fmt.Errorf("failed to register service: %w", err)
	}
	defer listener.Close()

	log.Info().Msg("")
	log.Info().Msg("=== Service is now publicly accessible ===")
	log.Info().Msg("Access via:")
	log.Info().Msgf("- Name:     /peer/%s", flagName)
	log.Info().Msgf("- Lease ID: /peer/%s", leaseID)
	relayHost := extractHost(flagRelayURL)
	log.Info().Msgf("- Example:  http://%s/peer/%s", relayHost, flagName)
	log.Info().Msg("")
	log.Info().Msg("Press Ctrl+C to stop...")
	log.Info().Msg("")

	// Handle connections
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Info().Msg("")
		log.Info().Msg("Shutting down tunnel...")
		cancel()
	}()

	// Accept connections and proxy them
	connCount := 0
	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("Tunnel stopped")
			return nil
		default:
		}

		relayConn, err := listener.Accept()
		if err != nil {
			// Check if context was cancelled
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Error().Err(err).Msg("Failed to accept connection")
				continue
			}
		}

		connCount++
		currentConnCount := connCount
		log.Info().Msgf("→ [#%d] New connection from %s", currentConnCount, relayConn.RemoteAddr())

		// Handle connection in goroutine
		go func(relayConn net.Conn, connNum int) {
			if err := proxyConnection(relayConn, localAddr, connNum); err != nil {
				log.Error().Err(err).Int("conn", connNum).Msg("Proxy error")
			}
			log.Info().Msgf("← [#%d] Connection closed", connNum)
		}(relayConn, currentConnCount)
	}
}

func proxyConnection(relayConn net.Conn, localAddr string, connNum int) error {
	defer relayConn.Close()

	// Connect to local service
	localConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to local service: %w", err)
	}
	defer localConn.Close()

	// Bidirectional copy
	errCh := make(chan error, 2)

	// Relay -> Local
	go func() {
		_, err := io.Copy(localConn, relayConn)
		errCh <- err
	}()

	// Local -> Relay
	go func() {
		_, err := io.Copy(relayConn, localConn)
		errCh <- err
	}()

	// Wait for one direction to finish
	err = <-errCh

	// Close both connections to stop the other goroutine
	relayConn.Close()
	localConn.Close()

	// Wait for other goroutine
	<-errCh

	return err
}

func extractHost(wsURL string) string {
	// Simple extraction: ws://host:port/path -> host:port
	// Remove ws:// or wss://
	host := wsURL
	if len(host) > 5 && host[:5] == "ws://" {
		host = host[5:]
	} else if len(host) > 6 && host[:6] == "wss://" {
		host = host[6:]
	}

	// Remove path
	if idx := len(host); idx > 0 {
		for i, c := range host {
			if c == '/' {
				idx = i
				break
			}
		}
		host = host[:idx]
	}

	return host
}

// waitForLocalService tries to connect repeatedly until success or timeout.
// If timeout == 0, it waits indefinitely.
func waitForLocalService(localAddr string, timeout, interval time.Duration) error {
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		conn, err := net.DialTimeout("tcp", localAddr, 2*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for local service at %s: %w", localAddr, err)
		}
		time.Sleep(interval)
	}
}
