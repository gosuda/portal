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
)

type Config struct {
	_ struct{} `version:"0.0.1" command:"portal-tunnel" about:"Expose local services through Portal relay"`

	RelayURLs string `flag:"relay" env:"RELAYS" default:"http://localhost:4017" about:"Portal relay server API URLs (comma-separated, http/https)"`
	Host      string `flag:"host" env:"APP_HOST" about:"Target host to proxy to (host:port or URL)"`
	Name      string `flag:"name" env:"APP_NAME" about:"Service name"`

	// TLS Mode
	TLSEnable bool `flag:"tls" env:"TLS_ENABLE" default:"true" about:"Enable TLS termination on tunnel client (uses relay ACME DNS-01)"`

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

	relayURLs := parseURLs(cfg.RelayURLs)
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
		clientOpts = append(clientOpts, sdk.WithTLS())
		log.Info().Str("service", cfg.Name).Msg("TLS: Using relay ACME DNS-01 (E2EE)")
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
		log.Info().Str("service", cfg.Name).Msg("- TLS:      Enabled")
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
			if err := proxyConnection(ctx, cfg.Host, relayConn, cfg.TLSEnable); err != nil {
				log.Error().Str("service", cfg.Name).Str("proxy", proxyType).Err(err).Msg("Proxy error")
			}
			log.Info().Str("service", cfg.Name).Str("proxy", proxyType).Msg("Connection closed")
		}(relayConn)
	}
}

// parseURLs splits a comma-separated string into a list of trimmed, non-empty URLs.
func parseURLs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
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

// bufferPool provides reusable 64KB buffers for io.CopyBuffer to eliminate
// per-copy allocations and reduce GC pressure under high concurrency.
// Using *[]byte to avoid interface boxing allocation in sync.Pool.
var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

// proxyConnection proxies data between relay and local service using raw TCP.
// It ensures complete data transfer before closing connections.
func proxyConnection(ctx context.Context, localAddr string, relayConn net.Conn, tlsEnabled bool) error {
	defer relayConn.Close()

	// Try to connect to local service (no retry)
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	localConn, err := dialer.DialContext(ctx, "tcp", localAddr)
	if err != nil {
		log.Debug().
			Str("local_addr", localAddr).
			Err(err).
			Msg("Local service unavailable")
		if tlsEnabled {
			return fmt.Errorf("local service unavailable: %w", err)
		}
		return writeEmptyHTTPResponse(relayConn)
	}
	defer localConn.Close()

	log.Info().Str("local_addr", localAddr).Msg("Connected to local service")

	// Use bidirectional copy with proper error handling
	errCh := make(chan error, 2)
	stopCh := make(chan struct{})

	// Context cancellation handler
	go func() {
		select {
		case <-ctx.Done():
			relayConn.Close()
			localConn.Close()
		case <-stopCh:
		}
	}()

	// Relay -> Local
	go func() {
		bufPtr := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bufPtr)
		_, err := io.CopyBuffer(localConn, relayConn, *bufPtr)
		if err != nil {
			log.Debug().Err(err).Msg("relay->local copy ended")
		}
		// Shut down localConn write side to signal EOF to local service
		if tcpConn, ok := localConn.(*net.TCPConn); ok {
			tcpConn.CloseWrite()
		}
		errCh <- err
	}()

	// Local -> Relay
	go func() {
		bufPtr := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bufPtr)
		_, err := io.CopyBuffer(relayConn, localConn, *bufPtr)
		if err != nil {
			log.Debug().Err(err).Msg("local->relay copy ended")
		}
		errCh <- err
	}()

	// Wait for both directions to finish
	var firstErr error
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}

	close(stopCh)
	return firstErr
}

// writeEmptyHTTPResponse writes an HTML response indicating the service is unavailable.
// Used when the local service is unavailable to avoid showing browser error pages.
func writeEmptyHTTPResponse(conn net.Conn) error {
	htmlBody := `<!DOCTYPE html>
<html>
<head><title>Service Unavailable</title></head>
<body style="font-family:sans-serif;text-align:center;padding:50px;">
<h1>🔌 Service Unavailable</h1>
<p>The local service is not currently running.</p>
<p>Please start your local application and refresh this page.</p>
</body>
</html>`
	response := fmt.Sprintf("HTTP/1.1 503 Service Unavailable\r\n"+
		"Content-Type: text/html; charset=utf-8\r\n"+
		"Content-Length: %d\r\n"+
		"Connection: close\r\n"+
		"\r\n%s", len(htmlBody), htmlBody)
	_, err := conn.Write([]byte(response))
	return err
}
