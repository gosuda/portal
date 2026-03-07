package main

import (
	"context"
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
)

var (
	flagServerURL string
	flagPort      int
	flagName      string
	flagDesc      string
	flagTags      string
	flagOwner     string
	flagHide      bool
	flagThumbnail string
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	logger := log.With().Str("component", "demo-app").Logger()

	flag.StringVar(&flagServerURL, "server-url", "https://localhost:4017", "relay API URLs (comma-separated, https only)")
	flag.IntVar(&flagPort, "port", 8092, "local demo HTTP port")
	flag.StringVar(&flagName, "name", "demo-app", "backend display name")
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sdkClient, err := sdk.NewClient(sdk.ClientConfig{RelayURLs: sdk.SplitCSV(flagServerURL)})
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	defer sdkClient.Close()

	listener, err := sdkClient.Listen(ctx, sdk.ListenRequest{
		Name: flagName,
		Metadata: types.LeaseMetadata{
			Description: flagDesc,
			Tags:        sdk.SplitCSV(flagTags),
			Owner:       flagOwner,
			Thumbnail:   flagThumbnail,
			Hide:        flagHide,
		},
	})
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	if err := sdk.RunHTTP(ctx, listener, newHandler(), sdk.HTTPServeOptions{
		LocalAddr: fmt.Sprintf(":%d", flagPort),
	}); err != nil {
		return err
	}

	logger.Info().Strs("public_urls", listener.PublicURLs()).Int("local_port", flagPort).Msg("demo app registered with relay")
	if ctx.Err() != nil {
		logger.Info().Msg("demo app shutting down")
	}
	logger.Info().Msg("demo app shutdown complete")
	return nil
}
