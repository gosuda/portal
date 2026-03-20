package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
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
	flagName          string
	flagDesc          string
	flagTags          string
	flagOwner         string
	flagHide          bool
	flagThumbnail     string
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	logger := log.With().Str("component", "demo-udp").Logger()

	flag.StringVar(&flagRelayURLs, "relays", "https://localhost:4017", "additional relay API URLs (comma-separated; scheme omitted defaults to https; appended to registry.json defaults unless --default-relays=false is set) [env: RELAYS]")
	flag.BoolVar(&flagDefaultRelays, "default-relays", utils.ParseBoolEnv("DEFAULT_RELAYS", false), "include repository registry.json default relays [env: DEFAULT_RELAYS]")
	flag.StringVar(&flagName, "name", "demo-udp", "public hostname prefix (single DNS label)")
	flag.StringVar(&flagDesc, "description", "Portal demo UDP echo service", "lease description")
	flag.StringVar(&flagTags, "tags", "demo,udp,echo", "comma-separated lease tags")
	flag.StringVar(&flagOwner, "owner", "PortalApp Developer", "lease owner")
	flag.StringVar(&flagThumbnail, "thumbnail", "", "lease thumbnail")
	flag.BoolVar(&flagHide, "hide", true, "hide this lease from listings")

	flag.Parse()

	if err := runDemoUDP(); err != nil {
		logger.Error().Err(err).Msg("demo udp command failed")
		os.Exit(1)
	}
}

func runDemoUDP() error {
	logger := log.With().Str("component", "demo-udp").Logger()
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

	exposure, err := sdk.Expose(ctx, relayURLs, flagName, true, types.LeaseMetadata{
		Description: flagDesc,
		Tags:        utils.SplitCSV(flagTags),
		Owner:       flagOwner,
		Thumbnail:   flagThumbnail,
		Hide:        flagHide,
	})
	if err != nil {
		return fmt.Errorf("exposure listen error: %w", err)
	}
	if exposure == nil {
		return errors.New("demo udp requires at least one relay")
	}
	defer exposure.Close()

	udpAddrs, err := exposure.WaitDatagramReady(ctx)
	if err != nil {
		return fmt.Errorf("wait for udp readiness: %w", err)
	}
	for _, udpAddr := range udpAddrs {
		logger.Info().Str("udp_addr", udpAddr).Msg("demo udp relay ready")
	}

	go runUDPEchoLoop(ctx, exposure, logger)

	if err := exposure.RunHTTP(ctx, newInfoHandler(exposure), ""); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			err = nil
		}
		return err
	}

	if ctx.Err() != nil {
		logger.Info().Msg("demo udp shutting down")
	}
	logger.Info().Msg("demo udp shutdown complete")
	return nil
}

func runUDPEchoLoop(ctx context.Context, exposure *sdk.Exposure, logger zerolog.Logger) {
	for {
		frame, _, _, _, reply, err := exposure.AcceptDatagram()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			logger.Warn().Err(err).Msg("demo udp accept failed")
			return
		}

		payload := append([]byte(nil), frame.Payload...)
		if len(payload) == 0 {
			payload = []byte("pong")
		}
		if err := reply(payload); err != nil && ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
			logger.Warn().Err(err).Uint32("flow_id", frame.FlowID).Msg("demo udp reply failed")
			return
		}
	}
}

func newInfoHandler(exposure *sdk.Exposure) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message":   "demo-udp is running",
			"udp_addrs": exposure.UDPAddrs(),
		})
	})
	return mux
}
