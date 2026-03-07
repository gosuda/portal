package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"

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

	relayURLs := sdk.SplitCSV(flagRelayURLs)
	if len(relayURLs) == 0 {
		return errors.New("no relay URLs provided")
	}

	targetAddr, err := normalizeTargetAddr(flagHost)
	if err != nil {
		return fmt.Errorf("invalid --host value %q: %w", flagHost, err)
	}

	logger.Info().
		Str("local", targetAddr).
		Int("relay_count", len(relayURLs)).
		Strs("relays", relayURLs).
		Msg("starting portal tunnel")

	listenReq := sdk.ListenRequest{
		Name: flagName,
		Metadata: types.LeaseMetadata{
			Description: flagDesc,
			Tags:        sdk.SplitCSV(flagTags),
			Owner:       flagOwner,
			Thumbnail:   flagThumbnail,
			Hide:        flagHide,
		},
	}

	client, err := sdk.NewClient(sdk.ClientConfig{RelayURLs: relayURLs})
	if err != nil {
		return fmt.Errorf("service %s: failed to create client: %w", flagName, err)
	}
	defer client.Close()

	listener, err := client.Listen(ctx, listenReq)
	if err != nil {
		return fmt.Errorf("service %s: failed to start listener: %w", flagName, err)
	}
	defer listener.Close()

	for _, entry := range listener.Entries() {
		logger.Info().
			Str("relay", entry.RelayURL).
			Str("lease_id", entry.LeaseID).
			Strs("public_urls", entry.PublicURLs()).
			Msg("relay tunnel ready")
	}

	var connGroup errgroup.Group
	var connCount atomic.Int64
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		if err := runProxyLoop(groupCtx, listener, targetAddr, &connGroup, &connCount); err != nil {
			return fmt.Errorf("relay accept loop: %w", err)
		}
		return nil
	})
	group.Go(func() error {
		<-groupCtx.Done()
		if err := listener.Close(); err != nil {
			return fmt.Errorf("listener close: %w", err)
		}
		return nil
	})

	waitErr := group.Wait()
	if waitErr != nil {
		logger.Error().Err(waitErr).Msg("relay supervisor exited with error")
	}

	if ctx.Err() != nil {
		logger.Info().Msg("tunnel shutting down")
	}

	done := make(chan error, 1)
	go func() {
		done <- connGroup.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			logger.Error().Err(err).Msg("proxy connection group failed")
			waitErr = errors.Join(waitErr, err)
		}
	case <-time.After(5 * time.Second):
		logger.Warn().Msg("tunnel shutdown timeout; connections still active")
	}

	logger.Info().Msg("tunnel shutdown complete")
	return waitErr
}
