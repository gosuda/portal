package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/sdk"
	"gosuda.org/portal/types"
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
	flagTLS       bool
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	defaultRelayURLs := os.Getenv("RELAYS")
	if defaultRelayURLs == "" {
		defaultRelayURLs = "https://localhost:4017"
	}

	flag.StringVar(&flagRelayURLs, "relay", defaultRelayURLs, "Portal relay server API URLs (comma-separated, https only) [env: RELAYS]")
	flag.StringVar(&flagHost, "host", os.Getenv("APP_HOST"), "Target host to proxy to (host:port or URL) [env: APP_HOST]")
	flag.StringVar(&flagName, "name", os.Getenv("APP_NAME"), "Service name [env: APP_NAME]")

	tlsEnv := strings.TrimSpace(os.Getenv("TLS"))
	defaultTLS := true
	if tlsEnv != "" {
		defaultTLS = strings.EqualFold(tlsEnv, "true") || tlsEnv == "1"
	}
	flag.BoolVar(&flagTLS, "tls", defaultTLS, "Enable TLS (keyless) [env: TLS]")

	flag.StringVar(&flagDesc, "description", os.Getenv("APP_DESCRIPTION"), "Service description metadata [env: APP_DESCRIPTION]")
	flag.StringVar(&flagTags, "tags", os.Getenv("APP_TAGS"), "Service tags metadata (comma-separated) [env: APP_TAGS]")
	flag.StringVar(&flagThumbnail, "thumbnail", os.Getenv("APP_THUMBNAIL"), "Service thumbnail URL metadata [env: APP_THUMBNAIL]")
	flag.StringVar(&flagOwner, "owner", os.Getenv("APP_OWNER"), "Service owner metadata [env: APP_OWNER]")

	defaultHide := os.Getenv("APP_HIDE") == "true"
	flag.BoolVar(&flagHide, "hide", defaultHide, "Hide service from discovery (metadata) [env: APP_HIDE]")
	flag.Parse()

	if err := runTunnel(); err != nil {
		log.Error().Err(err).Msg("Exited with error")
		os.Exit(1)
	}
}

func runTunnel() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	relayURLs := types.ParseURLs(flagRelayURLs)
	if len(relayURLs) == 0 {
		return errors.New("no relay URLs provided")
	}
	if !flagTLS {
		return errors.New("reverse connect architecture requires TLS; set --tls=true")
	}
	if err := validateRelayURLsForReverseConnect(relayURLs); err != nil {
		return err
	}

	log.Info().Msgf("Local service is reachable at %s", flagHost)
	log.Info().Msg("Starting Portal Tunnel...")
	log.Info().Msgf("  Local:    %s", flagHost)
	log.Info().Msgf("  Relays:   %s", strings.Join(relayURLs, ", "))
	log.Info().Msgf("  TLS:      %t", flagTLS)

	opts := []sdk.ClientOption{sdk.WithBootstrapServers(relayURLs)}
	sdkClient, err := sdk.NewClient(opts...)
	if err != nil {
		return fmt.Errorf("service %s: failed to create client: %w", flagName, err)
	}
	defer sdkClient.Close()

	listener, err := sdkClient.Listen(
		flagName,
		types.WithDescription(flagDesc),
		types.WithTags(types.ParseURLs(flagTags)),
		types.WithOwner(flagOwner),
		types.WithThumbnail(flagThumbnail),
		types.WithHide(flagHide),
	)
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
	if flagTLS {
		log.Info().Msg("- TLS:      Enabled")
	}

	log.Info().Str("service", flagName).Msg("")

	connCount := 0
	var connWG sync.WaitGroup

loop:
	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("[tunnel] shutting down...")
			break loop
		default:
		}

		relayConn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				log.Info().Msg("[tunnel] listener closed")
				break loop
			}
			select {
			case <-ctx.Done():
				break loop
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
			tlsEnabled := flagTLS
			proxyType := "TCP"
			if tlsEnabled {
				proxyType = "TLS→TCP"
			}
			if err := proxyConnection(ctx, flagHost, relayConn); err != nil {
				log.Error().Str("proxy", proxyType).Err(err).Msg("Proxy error")
			}
			log.Info().Str("proxy", proxyType).Msg("Connection closed")
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
		log.Warn().Msg("[tunnel] shutdown timeout, some connections still active")
	}

	log.Info().Msg("[tunnel] shutdown complete")
	return nil
}

func validateRelayURLsForReverseConnect(relayURLs []string) error {
	for _, relayURL := range relayURLs {
		parsedURL, err := url.Parse(relayURL)
		if err != nil {
			return fmt.Errorf("invalid relay URL %q: %w", relayURL, err)
		}
		if !strings.EqualFold(parsedURL.Scheme, "https") {
			return fmt.Errorf("reverse connect requires https relay URLs, got %q", relayURL)
		}
	}
	return nil
}

var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

func proxyConnection(ctx context.Context, localAddr string, relayConn net.Conn) error {
	defer relayConn.Close()

	targetAddr, err := types.NormalizeTargetAddr(localAddr)
	if err != nil {
		return fmt.Errorf("invalid --host value %q: %w", localAddr, err)
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	localConn, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		log.Debug().
			Str("addr", targetAddr).
			Err(err).
			Msg("Local service unavailable")
		return fmt.Errorf("local service unavailable: %w", err)
	}
	defer localConn.Close()

	log.Info().Str("addr", targetAddr).Msg("Connected to local service")

	errCh := make(chan error, 2)
	stopCh := make(chan struct{})

	go func() {
		select {
		case <-ctx.Done():
			if closeErr := relayConn.Close(); closeErr != nil {
				log.Debug().Err(closeErr).Msg("failed to close relay connection on shutdown")
			}
			if closeErr := localConn.Close(); closeErr != nil {
				log.Debug().Err(closeErr).Msg("failed to close local connection on shutdown")
			}
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
			if closeErr := tcpConn.CloseWrite(); closeErr != nil {
				log.Debug().Err(closeErr).Msg("failed to close local write side")
			}
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
