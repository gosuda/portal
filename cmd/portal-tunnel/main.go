package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"gosuda.org/portal/sdk"
)

var (
	flagConfigPath string
	flagService    string
)

type serviceContext struct {
	Name         string
	LocalAddr    string
	RelayServers []string
}

func main() {
	if len(os.Args) < 2 {
		printTunnelUsage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "expose":
		fs := flag.NewFlagSet("expose", flag.ExitOnError)
		fs.StringVar(&flagConfigPath, "config", "", "Path to portal-tunnel config file")
		fs.StringVar(&flagService, "service", "", "Specific service name to expose (defaults to first entry)")
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
	fmt.Println("  portal-tunnel expose --config <file> [--service <name>]")
}

func runExpose() error {
	if flagConfigPath == "" {
		return fmt.Errorf("--config is required")
	}

	cfg, err := LoadConfig(flagConfigPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	services, err := selectServices(cfg, flagService)
	if err != nil {
		return err
	}

	relayDir := NewRelayDirectory(cfg.Relays)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info().Msg("")
		log.Info().Msg("Shutting down tunnels...")
		cancel()
	}()

	errCh := make(chan error, len(services))
	var wg sync.WaitGroup

	for _, svc := range services {
		service := svc
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runServiceTunnel(ctx, relayDir, service); err != nil {
				errCh <- err
			}
		}()
	}

	doneCh := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case err := <-errCh:
		cancel()
		<-doneCh
		return err
	case <-ctx.Done():
		<-doneCh
		log.Info().Msg("Tunnel stopped")
		return nil
	case <-doneCh:
		return nil
	}
}

func proxyConnection(ctx context.Context, svcCtx *serviceContext, relayConn net.Conn, connNum int) error {
	defer relayConn.Close()

	// Connect to local service
	localConn, err := net.Dial("tcp", svcCtx.LocalAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to local service %s: %w", svcCtx.LocalAddr, err)
	}
	defer localConn.Close()

	// Bidirectional copy
	errCh := make(chan error, 2)
	cancelCopy := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			relayConn.Close()
			localConn.Close()
		case <-cancelCopy:
		}
	}()

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
	close(cancelCopy)

	// Wait for other goroutine
	<-errCh

	return err
}

func runServiceTunnel(ctx context.Context, relayDir *RelayDirectory, service *ServiceConfig) error {
	localAddr := service.Target
	bootstrapServers, err := relayDir.BootstrapServers(service.RelayPreference)
	if err != nil {
		return fmt.Errorf("service %s: resolve relay servers: %w", service.Name, err)
	}

	svcCtx := &serviceContext{
		Name:         service.Name,
		LocalAddr:    localAddr,
		RelayServers: bootstrapServers,
	}

	log.Info().Str("service", service.Name).Msgf("Waiting for local service at %s (interval=%v)...", localAddr, time.Second)
	if err := waitForLocalService(localAddr, 0, time.Second); err != nil {
		return fmt.Errorf("service %s: %w", service.Name, err)
	}
	log.Info().Str("service", service.Name).Msgf("✓ Local service is reachable at %s", localAddr)

	cred := sdk.NewCredential()
	leaseID := cred.ID()

	log.Info().Str("service", service.Name).Msgf("Starting Portal Tunnel (config=%s)...", flagConfigPath)
	log.Info().Str("service", service.Name).Msgf("  Local:    %s", localAddr)
	log.Info().Str("service", service.Name).Msgf("  Relays:   %s", strings.Join(bootstrapServers, ", "))
	log.Info().Str("service", service.Name).Msgf("  Lease ID: %s", leaseID)

	client, err := sdk.NewClient(func(c *sdk.RDClientConfig) {
		c.BootstrapServers = bootstrapServers
	})
	if err != nil {
		return fmt.Errorf("service %s: failed to connect to relay: %w", service.Name, err)
	}
	defer client.Close()

	listener, err := client.Listen(cred, service.Name, service.Protocols)
	if err != nil {
		return fmt.Errorf("service %s: failed to register service: %w", service.Name, err)
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	log.Info().Str("service", service.Name).Msg("")
	log.Info().Str("service", service.Name).Msg("=== Service is now publicly accessible ===")
	log.Info().Str("service", service.Name).Msg("Access via:")
	log.Info().Str("service", service.Name).Msgf("- Name:     /peer/%s", service.Name)
	log.Info().Str("service", service.Name).Msgf("- Lease ID: /peer/%s", leaseID)
	relayHost := extractHost(bootstrapServers[0])
	log.Info().Str("service", service.Name).Msgf("- Example:  http://%s/peer/%s", relayHost, service.Name)
	log.Info().Str("service", service.Name).Msg("")

	connCount := 0
	var connWG sync.WaitGroup
	defer connWG.Wait()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		relayConn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				log.Error().Str("service", service.Name).Err(err).Msg("Failed to accept connection")
				continue
			}
		}

		connCount++
		currentConnCount := connCount
		log.Info().Str("service", service.Name).Msgf("→ [#%d] New connection from %s", currentConnCount, relayConn.RemoteAddr())

		connWG.Add(1)
		go func(relayConn net.Conn, connNum int) {
			defer connWG.Done()
			if err := proxyConnection(ctx, svcCtx, relayConn, connNum); err != nil {
				log.Error().Str("service", service.Name).Err(err).Int("conn", connNum).Msg("Proxy error")
			}
			log.Info().Str("service", service.Name).Msgf("← [#%d] Connection closed", connNum)
		}(relayConn, currentConnCount)
	}
}

func selectServices(cfg *TunnelConfig, name string) ([]*ServiceConfig, error) {
	if len(cfg.Services) == 0 {
		return nil, fmt.Errorf("config has no services")
	}
	if name == "" {
		services := make([]*ServiceConfig, len(cfg.Services))
		for i := range cfg.Services {
			services[i] = &cfg.Services[i]
		}
		return services, nil
	}
	for i := range cfg.Services {
		if cfg.Services[i].Name == name {
			return []*ServiceConfig{&cfg.Services[i]}, nil
		}
	}
	return nil, fmt.Errorf("service %q not found in config", name)
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
