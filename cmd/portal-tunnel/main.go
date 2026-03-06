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

	"gosuda.org/portal/v2/sdk"
	"gosuda.org/portal/v2/types"
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

	flag.StringVar(&flagRelayURLs, "relays", defaultRelayURLs, "Portal relay server API URLs (comma-separated, https only) [env: RELAYS]")
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

	relayURLs, err := normalizeRelayURLs(flagRelayURLs)
	if err != nil {
		return err
	}
	if len(relayURLs) == 0 {
		return errors.New("no relay URLs provided")
	}

	logger.Info().
		Str("local", flagHost).
		Int("relay_count", len(relayURLs)).
		Strs("relays", relayURLs).
		Msg("starting portal tunnel")

	listenReq := sdk.ListenRequest{
		Name: flagName,
		Metadata: types.LeaseMetadata{
			Description: flagDesc,
			Tags:        splitCSV(flagTags),
			Owner:       flagOwner,
			Thumbnail:   flagThumbnail,
			Hide:        flagHide,
		},
	}

	runtimes, err := startRelayRuntimes(ctx, relayURLs, listenReq)
	if err != nil {
		return fmt.Errorf("service %s: failed to start relays: %w", flagName, err)
	}

	var connWG sync.WaitGroup
	var connCount atomic.Int64
	relayDone := make(chan relayLoopResult, len(runtimes))

	for _, runtime := range runtimes {
		logger.Info().
			Str("relay", runtime.relayURL).
			Str("lease_id", runtime.listener.LeaseID()).
			Strs("public_urls", runtime.listener.PublicURLs()).
			Msg("relay tunnel ready")
		go runtime.run(ctx, flagHost, &connWG, &connCount, relayDone)
	}

	waitErr := waitForRelayLoops(ctx, relayDone, len(runtimes))
	if waitErr != nil {
		stop()
	}
	closeErr := closeRelayRuntimes(runtimes)
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
