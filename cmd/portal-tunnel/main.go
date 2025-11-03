package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"gosuda.org/portal/sdk"
)

var (
	flagRelayURL  string
	flagLocalPort int
	flagName      string
	flagLocalHost string
)

var rootCmd = &cobra.Command{
	Use:   "portal-tunnel",
	Short: "Expose local services through Portal relay (like cloudflared tunnel)",
	Long: `Portal Tunnel exposes your local services to the internet through a secure Portal relay.

Example:
  portal-tunnel expose 8080 --name my-service
  portal-tunnel expose 3000 --name api --relay ws://my-relay.com/relay
`,
}

var exposeCmd = &cobra.Command{
	Use:   "expose [local-port]",
	Short: "Expose a local port through the relay",
	Args:  cobra.ExactArgs(1),
	RunE:  runExpose,
}

func init() {
	exposeCmd.Flags().StringVar(&flagRelayURL, "relay", "ws://localhost:4017/relay", "Portal relay server URL")
	exposeCmd.Flags().StringVar(&flagName, "name", "", "Service name (will be generated if not provided)")
	exposeCmd.Flags().StringVar(&flagLocalHost, "local-host", "localhost", "Local host to proxy to")

	rootCmd.AddCommand(exposeCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("Failed to execute command")
	}
}

func runExpose(cmd *cobra.Command, args []string) error {
	// Parse local port
	var port string = args[0]
	localAddr := fmt.Sprintf("%s:%s", flagLocalHost, port)

	// Test local service connectivity
	log.Info().Msgf("Testing connection to local service at %s...", localAddr)
	testConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("cannot connect to local service at %s: %w", localAddr, err)
	}
	testConn.Close()
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
	listener, err := client.Listen(cred, flagName, []string{"http/1.1", "h2"})
	if err != nil {
		return fmt.Errorf("failed to register service: %w", err)
	}
	defer listener.Close()

	log.Info().Msg("")
	log.Info().Msg("┌─────────────────────────────────────────────────────────────┐")
	log.Info().Msgf("│  🌐 Service is now publicly accessible!                     │")
	log.Info().Msg("├─────────────────────────────────────────────────────────────┤")
	log.Info().Msgf("│  Access via:                                                │")
	log.Info().Msgf("│    - Name:     /peer/%s                              │", padRight(flagName, 30))
	log.Info().Msgf("│    - Lease ID: /peer/%s    │", padRight(leaseID[:26], 30))
	log.Info().Msg("│                                                             │")
	log.Info().Msgf("│  Example:                                                   │")
	relayHost := extractHost(flagRelayURL)
	log.Info().Msgf("│    http://%s/peer/%s                 │", padRight(relayHost, 20), padRight(flagName, 20))
	log.Info().Msg("└─────────────────────────────────────────────────────────────┘")
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

func padRight(s string, length int) string {
	if len(s) >= length {
		return s
	}
	return s + string(make([]byte, length-len(s)))
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
