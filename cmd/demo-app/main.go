package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
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
	flagDefaultRelays bool
	flagAddr          string
	flagName          string
	flagDesc          string
	flagTags          string
	flagOwner         string
	flagHide          bool
	flagThumbnail     string
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	logger := log.With().Str("component", "demo-app").Logger()

	flag.StringVar(&flagRelayURLs, "relays", "https://localhost:4017", "additional relay API URLs (comma-separated; scheme omitted defaults to https; appended to registry.json defaults unless --default-relays=false is set) [env: RELAYS]")
	flag.BoolVar(&flagDefaultRelays, "default-relays", utils.ParseBoolEnv("DEFAULT_RELAYS", true), "include repository registry.json default relays [env: DEFAULT_RELAYS]")
	flag.StringVar(&flagAddr, "addr", "127.0.0.1:8092", "local demo HTTP listen address (host:port or URL; disable if empty)")
	flag.StringVar(&flagName, "name", "demo-app", "public hostname prefix (single DNS label)")
	flag.StringVar(&flagDesc, "description", "Portal demo connectivity app", "lease description")
	flag.StringVar(&flagTags, "tags", "demo,connectivity,activity,cloud,sun,morning", "comma-separated lease tags")
	flag.StringVar(&flagOwner, "owner", "PortalApp Developer", "lease owner")
	flag.StringVar(&flagThumbnail, "thumbnail", "https://picsum.photos/640/360", "lease thumbnail")
	flag.BoolVar(&flagHide, "hide", false, "hide this lease from listings")

	flag.Parse()

	if err := runDemo(); err != nil {
		logger.Error().Err(err).Msg("demo command failed")
		os.Exit(1)
	}
}

func runDemo() error {
	logger := log.With().Str("component", "demo-app").Logger()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGHUP)
	defer stop()

	relayURLs := utils.SplitCSV(flagRelayURLs)
	if flagDefaultRelays {
		relayURLs = sdk.WithDefaultRelayURLs(ctx, "", relayURLs...)
	}
	relayURLs, err := utils.NormalizeRelayURLs(relayURLs)
	if err != nil {
		return fmt.Errorf("resolve relay urls: %w", err)
	}

	exposure, err := sdk.Expose(ctx, relayURLs, flagName, types.TransportTCP, types.LeaseMetadata{
		Description: flagDesc,
		Tags:        utils.SplitCSV(flagTags),
		Owner:       flagOwner,
		Thumbnail:   flagThumbnail,
		Hide:        flagHide,
	})
	if err != nil {
		return fmt.Errorf("exposure listen error: %w", err)
	}
	defer exposure.Close()
	if exposure == nil {
		logger.Info().Msg("demo app running without relay")
	}

	flagAddr, err := utils.NormalizeTargetAddr(flagAddr)
	if err != nil {
		return fmt.Errorf("invalid --addr value %q: %w", flagAddr, err)
	}
	if err := exposure.RunHTTP(ctx, newHandler(), flagAddr); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			err = nil
		}
		return err
	}

	if ctx.Err() != nil {
		logger.Info().Msg("demo app shutting down")
	}
	logger.Info().Msg("demo app shutdown complete")
	return nil
}
