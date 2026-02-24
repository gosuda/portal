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
	"time"

	"github.com/rs/zerolog/log"
	"gopkg.eu.org/broccoli"
	"gosuda.org/portal/sdk"
	"gosuda.org/portal/utils"
)

// bufferPool provides reusable 64KB buffers for io.CopyBuffer to eliminate
// per-copy allocations and reduce GC pressure under high concurrency.
// Using *[]byte to avoid interface boxing allocation in sync.Pool.
var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

type Config struct {
	_ struct{} `version:"0.0.1" command:"portal-tunnel" about:"Expose local services through Portal relay"`

	RelayURLs string `flag:"relay" env:"RELAYS" default:"http://localhost:4017" about:"Portal relay server API URLs (comma-separated, http/https)"`
	Host      string `flag:"host" env:"APP_HOST" about:"Target host to proxy to (host:port or URL)"`
	Name      string `flag:"name" env:"APP_NAME" about:"Service name"`

	// TLS Mode
	TLSEnable   bool   `flag:"tls" env:"TLS_ENABLE" about:"Enable TLS termination on tunnel client (requires TLS cert)"`
	TLSDomain   string `flag:"tls-domain" env:"TLS_DOMAIN" about:"Domain for TLS certificate (e.g., tunnel.example.com)"`
	TLSCert     string `flag:"tls-cert" env:"TLS_CERT" about:"Path to TLS certificate file (optional, uses autocert if not set)"`
	TLSKey      string `flag:"tls-key" env:"TLS_KEY" about:"Path to TLS key file (optional, uses autocert if not set)"`
	TLSAutocert bool   `flag:"tls-autocert" env:"TLS_AUTOCERT" about:"Use Let's Encrypt autocert for TLS (default: true if TLS enabled)"`

	// Metadata
	Protocols   string `flag:"protocols" env:"APP_PROTOCOLS" default:"http/1.1,h2" about:"ALPN protocols (comma-separated)"`
	Description string `flag:"description" env:"APP_DESCRIPTION" about:"Service description metadata"`
	Tags        string `flag:"tags" env:"APP_TAGS" about:"Service tags metadata (comma-separated)"`
	Thumbnail   string `flag:"thumbnail" env:"APP_THUMBNAIL" about:"Service thumbnail URL metadata"`
	Owner       string `flag:"owner" env:"APP_OWNER" about:"Service owner metadata"`
	Hide        bool   `flag:"hide" env:"APP_HIDE" about:"Hide service from discovery (metadata)"`
}

func main() {
	var cfg Config
	app, err := broccoli.NewApp(&cfg)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create app")
		os.Exit(1)
	}

	if _, _, err = app.Bind(&cfg, os.Args[1:]); err != nil {
		if err == broccoli.ErrHelp {
			fmt.Println(app.Help())
			os.Exit(0)
		}

		fmt.Println(app.Help())
		log.Error().Err(err).Msg("Failed to bind CLI arguments")
		os.Exit(1)
	}

	if cfg.Host == "" || cfg.Name == "" {
		fmt.Println(app.Help())
		os.Exit(1)
	}

	relayURLs := utils.ParseURLs(cfg.RelayURLs)
	if len(relayURLs) == 0 {
		log.Error().Msg("--relay must include at least one non-empty URL")
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	go func() {
		<-sigCh
		log.Info().Msg("Shutting down tunnel...")
		cancel()
	}()

	if err := runServiceTunnel(ctx, relayURLs, cfg, "flags"); err != nil {
		log.Error().Err(err).Msg("Exited with error")
		os.Exit(1)
	}

	log.Info().Msg("Tunnel stopped")
}

func runServiceTunnel(ctx context.Context, relayURLs []string, cfg Config, origin string) error {
	if len(relayURLs) == 0 {
		return fmt.Errorf("no relay URLs provided")
	}

	log.Info().Str("service", cfg.Name).Msgf("Local service is reachable at %s", cfg.Host)
	log.Info().Str("service", cfg.Name).Msgf("Starting Portal Tunnel (%s)...", origin)
	log.Info().Str("service", cfg.Name).Msgf("  Local:    %s", cfg.Host)
	log.Info().Str("service", cfg.Name).Msgf("  Relays:   %s", strings.Join(relayURLs, ", "))
	log.Info().Str("service", cfg.Name).Msgf("  TLS Mode: %v", cfg.TLSEnable)

	// Build SDK client options
	var clientOpts []sdk.ClientOption
	clientOpts = append(clientOpts, sdk.WithBootstrapServers(relayURLs))

	// Configure TLS if enabled
	if cfg.TLSEnable {
		if cfg.TLSDomain == "" {
			return fmt.Errorf("TLS enabled but domain not specified")
		}

		if cfg.TLSCert != "" && cfg.TLSKey != "" {
			// Use custom certificate
			clientOpts = append(clientOpts, sdk.WithTLSCert(cfg.TLSCert, cfg.TLSKey))
			log.Info().Str("service", cfg.Name).Msg("TLS: Using custom certificate")
		} else if cfg.TLSAutocert {
			// Use Let's Encrypt autocert
			clientOpts = append(clientOpts, sdk.WithTLS(cfg.TLSDomain))
			log.Info().Str("service", cfg.Name).Str("domain", cfg.TLSDomain).Msg("TLS: Using Let's Encrypt autocert")
		} else {
			return fmt.Errorf("TLS enabled but no certificate source configured (set --tls-autocert or provide --tls-cert and --tls-key)")
		}
	}

	client, err := sdk.NewClient(clientOpts...)
	if err != nil {
		return fmt.Errorf("service %s: failed to create client: %w", cfg.Name, err)
	}
	defer client.Close()

	// Create metadata options
	metadataOptions := []sdk.MetadataOption{
		sdk.WithDescription(cfg.Description),
		sdk.WithTags(splitCSV(cfg.Tags)),
		sdk.WithOwner(cfg.Owner),
		sdk.WithThumbnail(cfg.Thumbnail),
		sdk.WithHide(cfg.Hide),
	}

	// Create listener (with or without TLS based on config)
	listener, err := client.Listen(cfg.Name, metadataOptions...)
	if err != nil {
		return fmt.Errorf("service %s: failed to register service: %w", cfg.Name, err)
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	log.Info().Str("service", cfg.Name).Msg("")
	log.Info().Str("service", cfg.Name).Msg("Access via:")
	log.Info().Str("service", cfg.Name).Msgf("- Relay:    %s", relayURLs[0])
	if leaseAware, ok := listener.(interface{ LeaseID() string }); ok {
		log.Info().Str("service", cfg.Name).Msgf("- Lease ID: %s", leaseAware.LeaseID())
	}
	if cfg.TLSEnable {
		log.Info().Str("service", cfg.Name).Msgf("- TLS:      Enabled (%s)", cfg.TLSDomain)
	}

	log.Info().Str("service", cfg.Name).Msg("")

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
				log.Error().Str("service", cfg.Name).Err(err).Msg("Failed to accept connection")
				continue
			}
		}

		connCount++
		log.Info().Str("service", cfg.Name).Msgf("→ [#%d] New connection from %s", connCount, relayConn.RemoteAddr())

		connWG.Add(1)
		go func(relayConn net.Conn) {
			defer connWG.Done()
			proxyType := "TCP"
			if cfg.TLSEnable {
				proxyType = "TLS→TCP"
			}
			if err := proxyConnection(ctx, cfg.Host, relayConn); err != nil {
				log.Error().Str("service", cfg.Name).Str("proxy", proxyType).Err(err).Msg("Proxy error")
			}
			log.Info().Str("service", cfg.Name).Str("proxy", proxyType).Msg("Connection closed")
		}(relayConn)
	}
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// proxyConnection proxies data between relay and local service.
// If local service is not available, it retries with backoff instead of failing immediately.
func proxyConnection(ctx context.Context, localAddr string, relayConn net.Conn) error {
	defer relayConn.Close()

	// Try to connect to local service with retry
	var localConn net.Conn
	var err error

	maxRetries := 30
	retryDelay := 500 * time.Millisecond

	for i := 0; i < maxRetries; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		dialer := &net.Dialer{Timeout: 5 * time.Second}
		localConn, err = dialer.DialContext(ctx, "tcp", localAddr)
		if err == nil {
			break
		}

		if i == 0 {
			log.Warn().
				Str("local_addr", localAddr).
				Err(err).
				Msg("Local service not ready, retrying...")
		}

		time.Sleep(retryDelay)
	}

	if err != nil {
		return fmt.Errorf("local service unavailable: %w", err)
	}

	defer localConn.Close()

	log.Info().Str("local_addr", localAddr).Msg("Connected to local service")

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
		buf := *bufferPool.Get().(*[]byte)
		defer bufferPool.Put(&buf)
		_, err := io.CopyBuffer(localConn, relayConn, buf)
		errCh <- err
	}()

	go func() {
		buf := *bufferPool.Get().(*[]byte)
		defer bufferPool.Put(&buf)
		_, err := io.CopyBuffer(relayConn, localConn, buf)
		errCh <- err
	}()

	err = <-errCh
	close(stopCh)
	relayConn.Close()
	<-errCh

	return err
}
