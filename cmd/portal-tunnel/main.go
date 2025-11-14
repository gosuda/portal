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
	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
	"gosuda.org/portal/sdk"
)

var (
	flagRelayURL  string
	flagHost      string
	flagPort      string
	flagName      string
	flagProtocol  string
	flagRelayHost string
	flagUDPPort   int
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
		fs.StringVar(&flagProtocol, "protocol", "tcp", "Protocol: tcp or udp")
		fs.StringVar(&flagRelayHost, "relay-host", "", "Relay server host for UDP (auto-detected from relay URL if not provided)")
		fs.IntVar(&flagUDPPort, "relay-udp-port", 19132, "Relay server UDP port")
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
	fmt.Println("  portal-tunnel expose [--port PORT] [--protocol tcp|udp] [--relay URL] [--name NAME] [--host HOST]")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  # Expose TCP service (HTTP server)")
	fmt.Println("  portal-tunnel expose --port 8080 --protocol tcp")
	fmt.Println()
	fmt.Println("  # Expose UDP service (Minecraft Bedrock)")
	fmt.Println("  portal-tunnel expose --port 19132 --protocol udp")
}

func runExpose() error {
	// Validate protocol
	if flagProtocol != "tcp" && flagProtocol != "udp" {
		return fmt.Errorf("invalid protocol: %s (must be 'tcp' or 'udp')", flagProtocol)
	}

	localAddr := net.JoinHostPort(flagHost, flagPort)

	// Wait for local service (TCP only - UDP services don't need connection check)
	if flagProtocol == "tcp" {
		log.Info().Msgf("Waiting for local TCP service at %s (interval=%v)...", localAddr, time.Second)
		if err := waitForLocalService(localAddr, 0, time.Second); err != nil {
			return err
		}
		log.Info().Msgf("✓ Local service is reachable at %s", localAddr)
	} else {
		log.Info().Msgf("UDP mode - skipping connection check for %s", localAddr)
	}

	// Create credential
	cred := sdk.NewCredential()
	leaseID := cred.ID()

	// Use provided name or generate from lease ID
	if flagName == "" {
		flagName = fmt.Sprintf("tunnel-%s", leaseID[:8])
	}

	log.Info().Msgf("Starting Portal Tunnel...")
	log.Info().Msgf("  Protocol: %s", flagProtocol)
	log.Info().Msgf("  Local:    %s", localAddr)
	log.Info().Msgf("  Relay:    %s", flagRelayURL)
	log.Info().Msgf("  Name:     %s", flagName)
	log.Info().Msgf("  Lease ID: %s", leaseID)

	// Route to TCP or UDP implementation
	if flagProtocol == "tcp" {
		return runTCPTunnel(cred, leaseID, localAddr)
	} else {
		return runUDPTunnel(cred, leaseID, localAddr)
	}
}

func runTCPTunnel(cred *cryptoops.Credential, leaseID, localAddr string) error {
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
	// Extract hostname from WebSocket URL: ws://host:port/path -> host
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

	// Remove port if present (extract hostname only)
	if idx := len(host); idx > 0 {
		for i, c := range host {
			if c == ':' {
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

func runUDPTunnel(cred *cryptoops.Credential, leaseID, localAddr string) error {
	// Create SDK client for control plane
	client, err := sdk.NewClient(func(c *sdk.RDClientConfig) {
		c.BootstrapServers = []string{flagRelayURL}
	})
	if err != nil {
		return fmt.Errorf("failed to connect to relay: %w", err)
	}
	defer client.Close()

	// Register UDP lease
	lease := &rdverb.Lease{
		Identity: &rdsec.Identity{
			Id:        cred.ID(),
			PublicKey: cred.PublicKey(),
		},
		Name:     flagName,
		Alpn:     []string{"udp"},
		Protocol: rdverb.Protocol_PROTOCOL_UDP,
		Expires:  time.Now().Add(1 * time.Hour).Unix(),
	}

	log.Info().Msgf("Registering UDP lease with relay server...")

	// Register lease with all relays
	err = client.RegisterLease(cred, lease)
	if err != nil {
		return fmt.Errorf("failed to register UDP lease: %w", err)
	}

	// Get relay host for UDP connection
	relayHost := flagRelayHost
	if relayHost == "" {
		relayHost = extractHost(flagRelayURL)
	}
	relayUDPAddr := fmt.Sprintf("%s:%d", relayHost, flagUDPPort)

	log.Info().Msgf("UDP relay address: %s", relayUDPAddr)

	// Request UDP session via TCP
	sessionToken, udpPort, err := requestUDPSession(client, cred, leaseID)
	if err != nil {
		return fmt.Errorf("failed to request UDP session: %w", err)
	}

	log.Info().Msg("")
	log.Info().Msg("=== UDP Service is now publicly accessible ===")
	log.Info().Msgf("  Session Token: %x", sessionToken)
	log.Info().Msgf("  UDP Port: %d", udpPort)
	log.Info().Msgf("  Relay: %s", relayUDPAddr)
	log.Info().Msg("")
	log.Info().Msg("Press Ctrl+C to stop...")
	log.Info().Msg("")

	// Start UDP packet bridging
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Info().Msg("")
		log.Info().Msg("Shutting down UDP tunnel...")
		cancel()
	}()

	// Run UDP bridge
	return runUDPBridge(ctx, localAddr, relayUDPAddr, sessionToken, lease)
}

func requestUDPSession(client *sdk.RDClient, cred *cryptoops.Credential, leaseID string) ([16]byte, int, error) {
	var sessionToken [16]byte

	// Use the real SDK method
	token, udpPort, err := client.RequestUDPSession(cred, leaseID)
	if err != nil {
		return sessionToken, 0, fmt.Errorf("failed to request UDP session: %w", err)
	}

	log.Info().
		Str("lease_id", leaseID).
		Int("udp_port", udpPort).
		Msg("UDP session successfully registered with relay server")

	return token, udpPort, nil
}

func runUDPBridge(ctx context.Context, localAddr, relayAddr string, sessionToken [16]byte, lease *rdverb.Lease) error {
	// Parse addresses
	localUDPAddr, err := net.ResolveUDPAddr("udp", localAddr)
	if err != nil {
		return fmt.Errorf("invalid local address: %w", err)
	}

	relayUDPAddr, err := net.ResolveUDPAddr("udp", relayAddr)
	if err != nil {
		return fmt.Errorf("invalid relay address: %w", err)
	}

	// Create UDP connection to relay
	relayConn, err := net.DialUDP("udp", nil, relayUDPAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to relay: %w", err)
	}
	defer relayConn.Close()

	// Create local UDP socket
	localConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return fmt.Errorf("failed to create local UDP socket: %w", err)
	}
	defer localConn.Close()

	log.Info().Msgf("UDP bridge started")
	log.Info().Msgf("  Local service: %s", localAddr)
	log.Info().Msgf("  Relay server: %s", relayAddr)

	// Start keepalive sender
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Send keepalive to relay
				keepalive, err := portal.EncodeUDPPacket(
					portal.UDPPacketTypeKeepalive,
					sessionToken,
					nil,
				)
				if err != nil {
					log.Error().Err(err).Msg("Failed to encode keepalive")
					continue
				}

				_, err = relayConn.Write(keepalive)
				if err != nil {
					log.Error().Err(err).Msg("Failed to send keepalive")
				} else {
					log.Debug().Msg("Sent keepalive to relay")
				}
			}
		}
	}()

	// Goroutine to forward packets from relay to local service
	go func() {
		buffer := make([]byte, 65507)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			relayConn.SetReadDeadline(time.Now().Add(1 * time.Second))
			n, err := relayConn.Read(buffer)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				log.Error().Err(err).Msg("Error reading from relay")
				continue
			}

			// Parse packet
			packet, err := portal.ParseUDPPacket(buffer[:n])
			if err != nil {
				log.Error().Err(err).Msg("Failed to parse packet from relay")
				continue
			}

			// Forward to local service
			_, err = localConn.WriteToUDP(packet.Data, localUDPAddr)
			if err != nil {
				log.Error().Err(err).Msg("Failed to forward to local service")
				continue
			}

			log.Info().Int("bytes", len(packet.Data)).Msg("📥 Relay → Local")
		}
	}()

	// Main goroutine: forward packets from local service to relay
	buffer := make([]byte, 65507)
	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("UDP tunnel stopped")
			return nil
		default:
		}

		localConn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, addr, err := localConn.ReadFromUDP(buffer)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			log.Error().Err(err).Msg("Error reading from local socket")
			continue
		}

		// Only accept packets from local service
		if addr.IP.IsLoopback() || addr.IP.Equal(net.IPv4zero) {
			// Encode with session token
			packet, err := portal.EncodeUDPPacket(
				portal.UDPPacketTypeData,
				sessionToken,
				buffer[:n],
			)
			if err != nil {
				log.Error().Err(err).Msg("Failed to encode packet")
				continue
			}

			// Forward to relay
			_, err = relayConn.Write(packet)
			if err != nil {
				log.Error().Err(err).Msg("Failed to forward to relay")
				continue
			}

			log.Info().Int("bytes", n).Msg("📤 Local → Relay")
		}
	}
}
