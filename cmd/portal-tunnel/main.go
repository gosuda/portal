package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/sdk"
	"github.com/gosuda/portal/v2/types"
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
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	logger := log.With().Str("component", "portal-tunnel").Logger()

	defaultRelayURLs := os.Getenv("RELAYS")
	if defaultRelayURLs == "" {
		defaultRelayURLs = "https://localhost:4017"
	}

	flag.StringVar(&flagRelayURLs, "relays", defaultRelayURLs, "Portal relay server API URLs (comma-separated; scheme omitted defaults to https) [env: RELAYS]")
	flag.StringVar(&flagHost, "host", os.Getenv("APP_HOST"), "Target host to proxy to (host:port or URL) [env: APP_HOST]")
	flag.StringVar(&flagName, "name", os.Getenv("APP_NAME"), "Service name [env: APP_NAME]")
	flag.StringVar(&flagDesc, "description", os.Getenv("APP_DESCRIPTION"), "Service description metadata [env: APP_DESCRIPTION]")
	flag.StringVar(&flagTags, "tags", os.Getenv("APP_TAGS"), "Service tags metadata (comma-separated) [env: APP_TAGS]")
	flag.StringVar(&flagThumbnail, "thumbnail", os.Getenv("APP_THUMBNAIL"), "Service thumbnail URL metadata [env: APP_THUMBNAIL]")
	flag.StringVar(&flagOwner, "owner", os.Getenv("APP_OWNER"), "Service owner metadata [env: APP_OWNER]")
	flag.BoolVar(&flagHide, "hide", os.Getenv("APP_HIDE") == "true", "Hide service from discovery (metadata) [env: APP_HIDE]")
	flag.Parse()

	if err := runTunnel(); err != nil {
		logger.Error().Err(err).Msg("portal tunnel exited with error")
		os.Exit(1)
	}
}

func runTunnel() error {
	logger := log.With().Str("component", "portal-tunnel").Logger()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	metadata := types.LeaseMetadata{
		Description: flagDesc,
		Tags:        sdk.SplitCSV(flagTags),
		Owner:       flagOwner,
		Thumbnail:   flagThumbnail,
		Hide:        flagHide,
	}

	logger.Info().
		Str("local", flagHost).
		Msg("starting portal tunnel")

	// TCP is the primary transport — failure is fatal.
	exposure, err := sdk.Expose(ctx, sdk.SplitCSV(flagRelayURLs), flagName, metadata)
	if err != nil {
		return fmt.Errorf("service %s: failed to start relays: %w", flagName, err)
	}
	if exposure == nil {
		return errors.New("no relay URLs provided")
	}
	defer exposure.Close()

	// UDP is best-effort — attach to existing lease, log and continue if it fails.
	go runUDPBestEffort(ctx, exposure)

	var connWG sync.WaitGroup
	var connCount atomic.Int64

	go func() {
		<-ctx.Done()
		_ = exposure.Close()
	}()

	waitErr := proxyRelayConnections(ctx, exposure, flagHost, &connWG, &connCount)
	if waitErr != nil {
		stop()
	}
	closeErr := exposure.Close()
	if waitErr != nil {
		logger.Error().Err(waitErr).Msg("relay supervisor exited with error")
	}
	if closeErr != nil {
		logger.Error().Err(closeErr).Msg("relay shutdown failed")
	}

	if ctx.Err() != nil {
		logger.Info().Msg("tunnel shutting down")
	}

	done := make(chan struct{})
	go func() {
		connWG.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		logger.Warn().Msg("tunnel shutdown timeout; connections still active")
	}

	logger.Info().Msg("tunnel shutdown complete")
	return errors.Join(waitErr, closeErr)
}

// runUDPBestEffort attaches UDP listeners to the existing TCP exposure lease.
// Failures are logged but do not bring down the tunnel.
func runUDPBestEffort(ctx context.Context, exposure *sdk.Exposure) {
	logger := log.With().Str("component", "portal-tunnel-udp").Logger()

	udpListeners, err := exposure.AttachUDP(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("udp transport disabled: attach failed")
		return
	}
	if len(udpListeners) == 0 {
		logger.Info().Msg("udp transport: no UDP addresses from relay")
		return
	}

	var wg sync.WaitGroup
	for _, ul := range udpListeners {
		wg.Add(1)
		go func(l *sdk.UDPListener) {
			defer wg.Done()
			defer l.Close()

			logger.Info().
				Str("udp_addr", l.UDPAddr()).
				Str("lease_id", l.LeaseID()).
				Msg("UDP tunnel ready")

			if err := proxyUDPRelayConnections(ctx, l, flagHost); err != nil {
				logger.Warn().Err(err).Msg("udp proxy ended")
			}
		}(ul)
	}
	wg.Wait()
}
