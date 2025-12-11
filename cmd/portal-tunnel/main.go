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
	"gopkg.eu.org/broccoli"
	"gosuda.org/portal/sdk"
	"gosuda.org/portal/utils"
)

// bufferPool provides reusable 64KB buffers for io.CopyBuffer to eliminate
// per-copy allocations and reduce GC pressure under high concurrency.
var bufferPool = sync.Pool{
	New: func() any { return make([]byte, 64*1024) },
}

type Config struct {
	_ struct{} `version:"0.0.1" command:"portal-tunnel" about:"Expose local apps through Portal relay"`

	ConfigPath string `flag:"config" alias:"c" env:"TUNNEL_CONFIG" about:"Path to portal-tunnel config file"`
	RelayURLs  string `flag:"relay" env:"RELAYS" default:"ws://localhost:4017/relay" about:"Portal relay server URLs when config is not provided (comma-separated)"`
	Host       string `flag:"host" env:"APP_HOST" about:"target host to proxy to when config is not provided (host:port or URL)"`
	Name       string `flag:"name" env:"APP_NAME" about:"App name when config is not provided"`

	// Metadata
	Description string `flag:"description" env:"APP_DESCRIPTION" about:"App description metadata"`
	Tags        string `flag:"tags" env:"APP_TAGS" about:"App tags metadata (comma-separated)"`
	Thumbnail   string `flag:"thumbnail" env:"APP_THUMBNAIL" about:"App thumbnail URL metadata"`
	Owner       string `flag:"owner" env:"APP_OWNER" about:"App owner metadata"`
	Hide        bool   `flag:"hide" env:"APP_HIDE" about:"Hide app from discovery (metadata)"`
}

func main() {
	var cfg Config
	app, err := broccoli.NewApp(&cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating app: %v\n", err)
		os.Exit(1)
	}

	_, _, err = app.Bind(&cfg, os.Args[1:])
	if err != nil {
		if err == broccoli.ErrHelp {
			fmt.Print(app.Help())
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n\n", err)
		fmt.Print(app.Help())
		os.Exit(1)
	}

	var runErr error
	if cfg.ConfigPath != "" {
		runErr = runExposeWithConfig(cfg.ConfigPath)
	} else {
		if cfg.Host == "" || cfg.Name == "" {
			fmt.Print(app.Help())
			os.Exit(1)
		}
		runErr = runExposeWithFlags(cfg)
	}

	if runErr != nil {
		log.Error().Err(runErr).Msg("Exited with error")
		os.Exit(1)
	}
}

func runExposeWithConfig(configPath string) error {
	cfg, err := LoadConfig(configPath)
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

	if err := runAppTunnel(ctx, relayURLs, &cfg.App, fmt.Sprintf("config=%s", configPath)); err != nil {
		return err
	}

	log.Info().Msg("Tunnel stopped")
	return nil
}

func runExposeWithFlags(cfg Config) error {
	relayURLs := utils.ParseURLs(cfg.RelayURLs)
	if len(relayURLs) == 0 {
		return fmt.Errorf("--relay must include at least one non-empty URL when --config is not provided")
	}

	var metadata sdk.Metadata
	if strings.TrimSpace(cfg.Description) != "" {
		metadata.Description = cfg.Description
	}
	if strings.TrimSpace(cfg.Tags) != "" {
		tags := strings.Split(cfg.Tags, ",")
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
	if strings.TrimSpace(cfg.Thumbnail) != "" {
		metadata.Thumbnail = cfg.Thumbnail
	}
	if strings.TrimSpace(cfg.Owner) != "" {
		metadata.Owner = cfg.Owner
	}
	if cfg.Hide {
		metadata.Hide = cfg.Hide
	}

	app := &AppConfig{
		Name:     strings.TrimSpace(cfg.Name),
		Target:   cfg.Host,
		Metadata: metadata,
	}

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

	if err := runAppTunnel(ctx, relayURLs, app, "flags"); err != nil {
		return err
	}

	log.Info().Msg("Tunnel stopped")
	return nil
}

func proxyConnection(ctx context.Context, localAddr string, relayConn net.Conn) error {
	defer relayConn.Close()

	localConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to local app %s: %w", localAddr, err)
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

func runAppTunnel(ctx context.Context, relayURLs []string, app *AppConfig, origin string) error {
	localAddr := app.Target
	appName := strings.TrimSpace(app.Name)
	if len(relayURLs) == 0 {
		return fmt.Errorf("no relay URLs provided")
	}
	bootstrapServers := relayURLs

	cred := sdk.NewCredential()
	leaseID := cred.ID()
	if appName == "" {
		appName = fmt.Sprintf("tunnel-%s", leaseID[:8])
		log.Info().Str("app", appName).Msg("No app name provided; generated automatically")
	}
	log.Info().Str("app", appName).Msgf("Local app is reachable at %s", localAddr)
	log.Info().Str("app", appName).Msgf("Starting Portal Tunnel (%s)...", origin)
	log.Info().Str("app", appName).Msgf("  Local:    %s", localAddr)
	log.Info().Str("app", appName).Msgf("  Relays:   %s", strings.Join(bootstrapServers, ", "))
	log.Info().Str("app", appName).Msgf("  Lease ID: %s", leaseID)

	client, err := sdk.NewClient(func(c *sdk.ClientConfig) {
		c.BootstrapServers = bootstrapServers
	})
	if err != nil {
		return fmt.Errorf("app %s: failed to connect to relay: %w", appName, err)
	}
	defer client.Close()

	listener, err := client.Listen(cred, appName, app.Protocols,
		sdk.WithDescription(app.Metadata.Description),
		sdk.WithTags(app.Metadata.Tags),
		sdk.WithOwner(app.Metadata.Owner),
		sdk.WithThumbnail(app.Metadata.Thumbnail),
		sdk.WithHide(app.Metadata.Hide),
	)
	if err != nil {
		return fmt.Errorf("app %s: failed to register app: %w", appName, err)
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	log.Info().Str("app", appName).Msg("")
	log.Info().Str("app", appName).Msg("Access via:")
	log.Info().Str("app", appName).Msgf("- Name:     /peer/%s", appName)
	log.Info().Str("app", appName).Msgf("- Lease ID: /peer/%s", leaseID)
	log.Info().Str("app", appName).Msgf("- Example:  http://%s/peer/%s", bootstrapServers[0], appName)

	log.Info().Str("app", appName).Msg("")

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
				log.Error().Str("app", appName).Err(err).Msg("Failed to accept connection")
				continue
			}
		}

		connCount++
		log.Info().Str("app", appName).Msgf("→ [#%d] New connection from %s", connCount, relayConn.RemoteAddr())

		connWG.Add(1)
		go func(relayConn net.Conn) {
			defer connWG.Done()
			if err := proxyConnection(ctx, localAddr, relayConn); err != nil {
				log.Error().Str("app", appName).Err(err).Msg("Proxy error")
			}
			log.Info().Str("app", appName).Msg("Connection closed")
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
