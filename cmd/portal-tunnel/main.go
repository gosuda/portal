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
	"github.com/gosuda/portal/v2/utils"
)

var (
	flagRelayURLs     string
	flagHost          string
	flagName          string
	flagDesc          string
	flagTags          string
	flagThumbnail     string
	flagOwner         string
	flagHide          bool
	flagDefaultRelays bool
)

func main() {
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	logger := log.With().Str("component", "portal-tunnel").Logger()

	defaultRelayURLs := os.Getenv("RELAYS")
	flag.StringVar(&flagRelayURLs, "relays", defaultRelayURLs, "Additional Portal relay server API URLs (comma-separated; scheme omitted defaults to https; appended to registry.json defaults unless --default-relays=false is set) [env: RELAYS]")
	flag.BoolVar(&flagDefaultRelays, "default-relays", utils.ParseBoolEnv("DEFAULT_RELAYS", true), "Include repository registry.json default relays [env: DEFAULT_RELAYS]")
	flag.StringVar(&flagHost, "host", os.Getenv("APP_HOST"), "Target host to proxy to (host:port or URL) [env: APP_HOST]")
	flag.StringVar(&flagName, "name", os.Getenv("APP_NAME"), "Public hostname prefix (single DNS label) [env: APP_NAME]")
	flag.StringVar(&flagDesc, "description", os.Getenv("APP_DESCRIPTION"), "Service description metadata [env: APP_DESCRIPTION]")
	flag.StringVar(&flagTags, "tags", os.Getenv("APP_TAGS"), "Service tags metadata (comma-separated) [env: APP_TAGS]")
	flag.StringVar(&flagThumbnail, "thumbnail", os.Getenv("APP_THUMBNAIL"), "Service thumbnail URL metadata [env: APP_THUMBNAIL]")
	flag.StringVar(&flagOwner, "owner", os.Getenv("APP_OWNER"), "Service owner metadata [env: APP_OWNER]")
	flag.BoolVar(&flagHide, "hide", utils.ParseBoolEnv("APP_HIDE", false), "Hide service from discovery (metadata) [env: APP_HIDE]")
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

	relayURLs := utils.SplitCSV(flagRelayURLs)
	if flagDefaultRelays {
		relayURLs = sdk.WithDefaultRelayURLs(ctx, relayURLs...)
	}
	relayURLs, err := utils.NormalizeRelayURLs(relayURLs)
	if err != nil {
		return fmt.Errorf("resolve relay urls: %w", err)
	}

	exposure, err := sdk.Expose(ctx, relayURLs, flagName, types.LeaseMetadata{
		Description: flagDesc,
		Tags:        utils.SplitCSV(flagTags),
		Owner:       flagOwner,
		Thumbnail:   flagThumbnail,
		Hide:        flagHide,
	})
	if err != nil {
		return fmt.Errorf("service %s: failed to start relays: %w", flagName, err)
	}
	if exposure == nil {
		return errors.New("no relay URLs provided")
	}
	defer exposure.Close()

	logger.Info().
		Str("local", flagHost).
		Msg("starting portal tunnel")

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
