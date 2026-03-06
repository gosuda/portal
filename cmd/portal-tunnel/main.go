package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/sdk"
)

var (
	flagRelayURLs string
	flagHost      string
	flagName      string
	flagDesc      string
	flagTags      string
	flagThumbnail string
	flagOwner     string
	flagHide      bool
)

func main() {
	defaultRelayURLs := os.Getenv("RELAYS")
	if defaultRelayURLs == "" {
		defaultRelayURLs = "https://localhost:4017"
	}

	flag.StringVar(&flagRelayURLs, "relay", defaultRelayURLs, "Portal relay server API URLs (comma-separated, https only) [env: RELAYS]")
	flag.StringVar(&flagHost, "host", os.Getenv("APP_HOST"), "Target host to proxy to (host:port or URL) [env: APP_HOST]")
	flag.StringVar(&flagName, "name", os.Getenv("APP_NAME"), "Service name [env: APP_NAME]")
	flag.StringVar(&flagDesc, "description", os.Getenv("APP_DESCRIPTION"), "Service description metadata [env: APP_DESCRIPTION]")
	flag.StringVar(&flagTags, "tags", os.Getenv("APP_TAGS"), "Service tags metadata (comma-separated) [env: APP_TAGS]")
	flag.StringVar(&flagThumbnail, "thumbnail", os.Getenv("APP_THUMBNAIL"), "Service thumbnail URL metadata [env: APP_THUMBNAIL]")
	flag.StringVar(&flagOwner, "owner", os.Getenv("APP_OWNER"), "Service owner metadata [env: APP_OWNER]")
	flag.BoolVar(&flagHide, "hide", os.Getenv("APP_HIDE") == "true", "Hide service from discovery (metadata) [env: APP_HIDE]")
	flag.Parse()

	if err := runTunnel(); err != nil {
		log.Printf("Exited with error: %v", err)
		os.Exit(1)
	}
}

func runTunnel() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	relayURLs := parseURLs(flagRelayURLs)
	if len(relayURLs) == 0 {
		return errors.New("no relay URLs provided")
	}
	relayURL, err := normalizeRelayURLsForReverseConnect(relayURLs)
	if err != nil {
		return err
	}

	log.Printf("Local service is reachable at %s", flagHost)
	log.Printf("Starting Portal Tunnel...")
	log.Printf("  Local:    %s", flagHost)
	log.Printf("  Relays:   %s", strings.Join(relayURLs, ", "))
	if len(relayURLs) > 1 {
		log.Printf("  Note: current runtime uses the first relay URL only: %s", relayURL)
	}

	sdkClient, err := sdk.NewClient(sdk.ClientConfig{RelayURL: relayURL})
	if err != nil {
		return fmt.Errorf("service %s: failed to create client: %w", flagName, err)
	}
	defer sdkClient.Close()

	listener, err := sdkClient.Listen(ctx, sdk.ListenRequest{
		Name: flagName,
		Metadata: sdk.LeaseMetadata{
			Description: flagDesc,
			Tags:        parseURLs(flagTags),
			Owner:       flagOwner,
			Thumbnail:   flagThumbnail,
			Hide:        flagHide,
		},
	})
	if err != nil {
		return fmt.Errorf("service %s: failed to register service: %w", flagName, err)
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	log.Printf("")
	log.Printf("Access via:")
	log.Printf("- Relay:    %s", relayURL)
	log.Printf("- Lease ID: %s", listener.LeaseID())
	for _, publicURL := range listener.PublicURLs() {
		log.Printf("- Public:   %s", publicURL)
	}
	log.Printf("")

	connCount := 0
	var connWG sync.WaitGroup

loop:
	for {
		select {
		case <-ctx.Done():
			log.Printf("[tunnel] shutting down...")
			break loop
		default:
		}

		relayConn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				log.Printf("[tunnel] listener closed")
				break loop
			}
			select {
			case <-ctx.Done():
				break loop
			default:
				log.Printf("Failed to accept connection: %v", err)
				continue
			}
		}

		connCount++
		log.Printf("[#%d] New connection from %s", connCount, relayConn.RemoteAddr())

		connWG.Add(1)
		go func(relayConn net.Conn) {
			defer connWG.Done()
			if err := proxyConnection(ctx, flagHost, relayConn); err != nil {
				log.Printf("Proxy error: %v", err)
			}
			log.Printf("Connection closed")
		}(relayConn)
	}

	done := make(chan struct{})
	go func() {
		connWG.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Printf("[tunnel] shutdown timeout, some connections still active")
	}

	log.Printf("[tunnel] shutdown complete")
	return nil
}

func normalizeRelayURLsForReverseConnect(relayURLs []string) (string, error) {
	return portal.NormalizeRelayURL(relayURLs[0])
}

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

func proxyConnection(ctx context.Context, localAddr string, relayConn net.Conn) error {
	defer relayConn.Close()

	targetAddr, err := normalizeTargetAddr(localAddr)
	if err != nil {
		return fmt.Errorf("invalid --host value %q: %w", localAddr, err)
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	localConn, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		return writeEmptyHTTPResponse(relayConn)
	}
	defer localConn.Close()

	errCh := make(chan error, 2)
	stopCh := make(chan struct{})

	go func() {
		select {
		case <-ctx.Done():
			_ = relayConn.Close()
			_ = localConn.Close()
		case <-stopCh:
		}
	}()

	go func() {
		bufPtr := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bufPtr)
		_, err := io.CopyBuffer(localConn, relayConn, *bufPtr)
		if tcpConn, ok := localConn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
		errCh <- err
	}()

	go func() {
		bufPtr := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(bufPtr)
		_, err := io.CopyBuffer(relayConn, localConn, *bufPtr)
		_ = relayConn.Close()
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
<h1>Service Unavailable</h1>
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

func parseURLs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
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

func normalizeTargetAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("target address is required")
	}
	if strings.Contains(raw, "://") {
		if strings.HasPrefix(strings.ToLower(raw), "http://") {
			raw = strings.TrimPrefix(raw, "http://")
		}
		if strings.HasPrefix(strings.ToLower(raw), "https://") {
			raw = strings.TrimPrefix(raw, "https://")
		}
		raw = strings.TrimSuffix(raw, "/")
	}
	if _, _, err := net.SplitHostPort(raw); err == nil {
		return raw, nil
	}
	if strings.Count(raw, ":") == 0 {
		return net.JoinHostPort(raw, "80"), nil
	}
	return "", fmt.Errorf("invalid target address %q", raw)
}
