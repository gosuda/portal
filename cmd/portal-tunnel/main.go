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

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/sdk"
)

var (
	flagRelayURLs   string
	flagHost        string
	flagName        string
	flagTLSEnable   bool
	flagProtocols   string
	flagDescription string
	flagTags        string
	flagThumbnail   string
	flagOwner       string
	flagHide        bool
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	defaultRelayURLs := os.Getenv("RELAYS")
	if defaultRelayURLs == "" {
		defaultRelayURLs = "http://localhost:4017"
	}

	flag.StringVar(&flagRelayURLs, "relay", defaultRelayURLs, "Portal relay server API URLs (comma-separated, http/https) [env: RELAYS]")
	flag.StringVar(&flagHost, "host", os.Getenv("APP_HOST"), "Target host to proxy to (host:port or URL) [env: APP_HOST]")
	flag.StringVar(&flagName, "name", os.Getenv("APP_NAME"), "Service name [env: APP_NAME]")

	defaultTLS := os.Getenv("TLS_ENABLE") != "false"
	flag.BoolVar(&flagTLSEnable, "tls", defaultTLS, "Enable TLS termination on tunnel client (uses relay ACME DNS-01) [env: TLS_ENABLE]")

	flag.StringVar(&flagProtocols, "protocols", os.Getenv("APP_PROTOCOLS"), "ALPN protocols (comma-separated) [env: APP_PROTOCOLS]")
	flag.StringVar(&flagDescription, "description", os.Getenv("APP_DESCRIPTION"), "Service description metadata [env: APP_DESCRIPTION]")
	flag.StringVar(&flagTags, "tags", os.Getenv("APP_TAGS"), "Service tags metadata (comma-separated) [env: APP_TAGS]")
	flag.StringVar(&flagThumbnail, "thumbnail", os.Getenv("APP_THUMBNAIL"), "Service thumbnail URL metadata [env: APP_THUMBNAIL]")
	flag.StringVar(&flagOwner, "owner", os.Getenv("APP_OWNER"), "Service owner metadata [env: APP_OWNER]")

	defaultHide := os.Getenv("APP_HIDE") == "true"
	flag.BoolVar(&flagHide, "hide", defaultHide, "Hide service from discovery (metadata) [env: APP_HIDE]")

	flag.Parse()

	if flagHost == "" || flagName == "" {
		flag.Usage()
		os.Exit(1)
	}

	relayURLs := parseURLs(flagRelayURLs)
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

	if err := runServiceTunnel(ctx, relayURLs); err != nil {
		log.Error().Err(err).Msg("Exited with error")
		os.Exit(1)
	}

	log.Info().Msg("Tunnel stopped")
}

func runServiceTunnel(ctx context.Context, relayURLs []string) error {
	if len(relayURLs) == 0 {
		return fmt.Errorf("no relay URLs provided")
	}

	log.Info().Msgf("Local service is reachable at %s", flagHost)
	log.Info().Msg("Starting Portal Tunnel...")
	log.Info().Msgf("  Local:    %s", flagHost)
	log.Info().Msgf("  Relays:   %s", strings.Join(relayURLs, ", "))
	log.Info().Msgf("  TLS Mode: %v", flagTLSEnable)

	var clientOpts []sdk.ClientOption
	clientOpts = append(clientOpts, sdk.WithBootstrapServers(relayURLs))

	if flagTLSEnable {
		clientOpts = append(clientOpts, sdk.WithTLS())
		log.Info().Msg("TLS: Using relay ACME DNS-01 (E2EE)")
	}

	client, err := sdk.NewClient(clientOpts...)
	if err != nil {
		return fmt.Errorf("service %s: failed to create client: %w", flagName, err)
	}
	defer client.Close()

	metadataOptions := []sdk.MetadataOption{
		sdk.WithDescription(flagDescription),
		sdk.WithTags(splitCSV(flagTags)),
		sdk.WithOwner(flagOwner),
		sdk.WithThumbnail(flagThumbnail),
		sdk.WithHide(flagHide),
	}

	listener, err := client.Listen(flagName, metadataOptions...)
	if err != nil {
		return fmt.Errorf("service %s: failed to register service: %w", flagName, err)
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	log.Info().Msg("")
	log.Info().Msg("Access via:")
	log.Info().Msgf("- Relay:    %s", relayURLs[0])
	if leaseAware, ok := listener.(interface{ LeaseID() string }); ok {
		log.Info().Msgf("- Lease ID: %s", leaseAware.LeaseID())
	}
	if flagTLSEnable {
		log.Info().Msg("- TLS:      Enabled")
	}

	log.Info().Str("service", flagName).Msg("")

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
				log.Error().Err(err).Msg("Failed to accept connection")
				continue
			}
		}

		connCount++
		log.Info().Msgf("→ [#%d] New connection from %s", connCount, relayConn.RemoteAddr())

		connWG.Add(1)
		go func(relayConn net.Conn) {
			defer connWG.Done()
			proxyType := "TCP"
			if flagTLSEnable {
				proxyType = "TLS→TCP"
			}
			if err := proxyConnection(ctx, flagHost, relayConn, flagTLSEnable); err != nil {
				log.Error().Str("proxy", proxyType).Err(err).Msg("Proxy error")
			}
			log.Info().Str("proxy", proxyType).Msg("Connection closed")
		}(relayConn)
	}
}

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

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

func proxyConnection(ctx context.Context, localAddr string, relayConn net.Conn, tlsEnabled bool) error {
	defer relayConn.Close()

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
		bufPtr := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bufPtr)
		_, err := io.CopyBuffer(localConn, relayConn, *bufPtr)
		if err != nil {
			log.Debug().Err(err).Msg("relay->local copy ended")
		}
		if tcpConn, ok := localConn.(*net.TCPConn); ok {
			tcpConn.CloseWrite()
		}
		errCh <- err
	}()

	go func() {
		bufPtr := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bufPtr)
		_, err := io.CopyBuffer(relayConn, localConn, *bufPtr)
		if err != nil {
			log.Debug().Err(err).Msg("local->relay copy ended")
		}
		errCh <- err
	}()

	var firstErr error
	for range 2 {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}

	close(stopCh)
	return firstErr
}

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
