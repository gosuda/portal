package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"gosuda.org/portal/sdk"
	"gosuda.org/portal/utils"
)

// bufferPool provides reusable 64KB buffers for io.CopyBuffer to eliminate
// per-copy allocations and reduce GC pressure under high concurrency.
var bufferPool = sync.Pool{
	New: func() any { return make([]byte, 64*1024) },
}

var (
	flagConfigPath string
	flagRelayURLs  string
	flagHost       string
	flagPort       string
	flagName       string
	flagDesc       string
	flagTags       string
	flagThumbnail  string
	flagOwner      string
	flagHide       bool
)

var rootCmd = &cobra.Command{
	Use:   "portal-tunnel",
	Short: "Expose local services through Portal relay",
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagConfigPath == "" {
			return runExposeWithFlags()
		}
		return runExposeWithConfig()
	},
}

func init() {
	rootCmd.Flags().StringVar(&flagConfigPath, "config", "", "Path to portal-tunnel config file")
	rootCmd.Flags().StringVar(&flagRelayURLs, "relay", "ws://localhost:4017/relay", "Portal relay server URLs when config is not provided (comma-separated)")
	rootCmd.Flags().StringVar(&flagHost, "host", "localhost", "Local host to proxy to when config is not provided")
	rootCmd.Flags().StringVar(&flagPort, "port", "4018", "Local port to proxy to when config is not provided")
	rootCmd.Flags().StringVar(&flagName, "name", "", "Service name when config is not provided (auto-generated if empty)")
	rootCmd.Flags().StringVar(&flagDesc, "description", "", "Service description metadata")
	rootCmd.Flags().StringVar(&flagTags, "tags", "", "Service tags metadata (comma-separated)")
	rootCmd.Flags().StringVar(&flagThumbnail, "thumbnail", "", "Service thumbnail URL metadata")
	rootCmd.Flags().StringVar(&flagOwner, "owner", "", "Service owner metadata")
	rootCmd.Flags().BoolVar(&flagHide, "hide", false, "Hide service from discovery (metadata)")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runExposeWithConfig() error {
	cfg, err := LoadConfig(flagConfigPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	relayURLs := normalizeRelayURLs(cfg.Relays)
	if len(relayURLs) == 0 {
		return fmt.Errorf("config: relays must include at least one URL")
	}

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

	if err := runServiceTunnel(ctx, relayURLs, &cfg.Service, fmt.Sprintf("config=%s", flagConfigPath)); err != nil {
		return err
	}

	log.Info().Msg("Tunnel stopped")
	return nil
}

func runExposeWithFlags() error {
	relayURLs := utils.ParseURLs(flagRelayURLs)
	if len(relayURLs) == 0 {
		return fmt.Errorf("--relay must include at least one non-empty URL when --config is not provided")
	}

	var metadata sdk.Metadata
	if strings.TrimSpace(flagDesc) != "" {
		metadata.Description = flagDesc
	}
	if strings.TrimSpace(flagTags) != "" {
		tags := strings.Split(flagTags, ",")
		for i := range tags {
			tags[i] = strings.TrimSpace(tags[i])
		}
		filtered := tags[:0]
		for _, t := range tags {
			if t != "" {
				filtered = append(filtered, t)
			}
		}
		metadata.Tags = filtered
	}
	if strings.TrimSpace(flagThumbnail) != "" {
		metadata.Thumbnail = flagThumbnail
	}
	if strings.TrimSpace(flagOwner) != "" {
		metadata.Owner = flagOwner
	}
	if flagHide {
		metadata.Hide = flagHide
	}

	target := net.JoinHostPort(flagHost, flagPort)
	service := &ServiceConfig{
		Name:     strings.TrimSpace(flagName),
		Target:   target,
		Metadata: metadata,
	}
	applyServiceDefaults(service)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info().Msg("")
		log.Info().Msg("Shutting down tunnel...")
		cancel()
	}()

	if err := runServiceTunnel(ctx, relayURLs, service, "flags"); err != nil {
		return err
	}

	log.Info().Msg("Tunnel stopped")
	return nil
}

func proxyConnection(ctx context.Context, localAddr string, relayConn net.Conn) error {
	defer relayConn.Close()

	localConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to local service %s: %w", localAddr, err)
	}
	defer localConn.Close()

	errCh := make(chan error, 2)
	stopCh := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			relayConn.Close()
			localConn.Close()
		case <-stopCh:
		}
	}()

	go func() {
		buf := bufferPool.Get().([]byte)
		defer bufferPool.Put(buf)
		_, err := io.CopyBuffer(localConn, relayConn, buf)
		errCh <- err
	}()

	go func() {
		buf := bufferPool.Get().([]byte)
		defer bufferPool.Put(buf)
		_, err := io.CopyBuffer(relayConn, localConn, buf)
		errCh <- err
	}()

	err = <-errCh
	close(stopCh)
	relayConn.Close()
	<-errCh

	return err
}

func runServiceTunnel(ctx context.Context, relayURLs []string, service *ServiceConfig, origin string) error {
	localAddr := service.Target
	serviceName := strings.TrimSpace(service.Name)
	if len(relayURLs) == 0 {
		return fmt.Errorf("no relay URLs provided")
	}
	bootstrapServers := relayURLs

	cred := sdk.NewCredential()
	leaseID := cred.ID()
	if serviceName == "" {
		serviceName = fmt.Sprintf("tunnel-%s", leaseID[:8])
		log.Info().Str("service", serviceName).Msg("No service name provided; generated automatically")
	}
	log.Info().Str("service", serviceName).Msgf("Local service is reachable at %s", localAddr)
	log.Info().Str("service", serviceName).Msgf("Starting Portal Tunnel (%s)...", origin)
	log.Info().Str("service", serviceName).Msgf("  Local:    %s", localAddr)
	log.Info().Str("service", serviceName).Msgf("  Relays:   %s", strings.Join(bootstrapServers, ", "))
	log.Info().Str("service", serviceName).Msgf("  Lease ID: %s", leaseID)

	client, err := sdk.NewClient(func(c *sdk.ClientConfig) {
		c.BootstrapServers = bootstrapServers
	})
	if err != nil {
		return fmt.Errorf("service %s: failed to connect to relay: %w", serviceName, err)
	}
	defer client.Close()

	listener, err := client.Listen(cred, serviceName, service.Protocols,
		sdk.WithDescription(service.Metadata.Description),
		sdk.WithTags(service.Metadata.Tags),
		sdk.WithOwner(service.Metadata.Owner),
		sdk.WithThumbnail(service.Metadata.Thumbnail),
		sdk.WithHide(service.Metadata.Hide),
	)
	if err != nil {
		return fmt.Errorf("service %s: failed to register service: %w", serviceName, err)
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	log.Info().Str("service", serviceName).Msg("")
	log.Info().Str("service", serviceName).Msg("Access via:")
	log.Info().Str("service", serviceName).Msgf("- Name:     /peer/%s", serviceName)
	log.Info().Str("service", serviceName).Msgf("- Lease ID: /peer/%s", leaseID)
	log.Info().Str("service", serviceName).Msgf("- Example:  http://%s/peer/%s", bootstrapServers[0], serviceName)

	log.Info().Str("service", serviceName).Msg("")

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
				log.Error().Str("service", serviceName).Err(err).Msg("Failed to accept connection")
				continue
			}
		}

		connCount++
		log.Info().Str("service", serviceName).Msgf("→ [#%d] New connection from %s", connCount, relayConn.RemoteAddr())

		connWG.Add(1)
		go func(relayConn net.Conn) {
			defer connWG.Done()
			if err := proxyConnection(ctx, localAddr, relayConn); err != nil {
				log.Error().Str("service", serviceName).Err(err).Msg("Proxy error")
			}
			log.Info().Str("service", serviceName).Msg("Connection closed")
		}(relayConn)
	}
}

// normalizeRelayURLs trims, de-duplicates, and filters empty relay URLs.
func normalizeRelayURLs(urls []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}
