package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/sdk"
)

var (
	flagPortalURL          string
	flagPortalSubdomainURL string
	flagBootstraps         []string
	flagALPN               string
	flagPort               int
	flagMaxLease           int
	flagLeaseBPS           int
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	defaultPortalURL := strings.TrimSuffix(os.Getenv("PORTAL_URL"), "/")
	if defaultPortalURL == "" {
		// Prefer explicit scheme for localhost so downstream URL building is unambiguous
		defaultPortalURL = "http://localhost:4017"
	}
	defaultSubdomain := os.Getenv("PORTAL_SUBDOMAIN_URL")
	if defaultSubdomain == "" {
		defaultSubdomain = sdk.DefaultSubdomainPattern(defaultPortalURL)
	}
	defaultBootstraps := os.Getenv("BOOTSTRAP_URIS")
	if defaultBootstraps == "" {
		defaultBootstraps = sdk.DefaultBootstrapFrom(defaultPortalURL)
	}

	var flagBootstrapsCSV string
	flag.StringVar(&flagPortalURL, "portal-url", defaultPortalURL, "base URL for portal frontend (env: PORTAL_URL)")
	flag.StringVar(&flagPortalSubdomainURL, "portal-subdomain-url", defaultSubdomain, "subdomain wildcard URL (env: PORTAL_SUBDOMAIN_URL)")
	flag.StringVar(&flagBootstrapsCSV, "bootstraps", defaultBootstraps, "bootstrap addresses (comma-separated)")
	flag.StringVar(&flagALPN, "alpn", "http/1.1", "ALPN identifier for this service")
	flag.IntVar(&flagPort, "port", 4017, "app UI and HTTP proxy port")
	flag.IntVar(&flagMaxLease, "max-lease", 0, "maximum active relayed connections per lease (0 = unlimited)")
	flag.IntVar(&flagLeaseBPS, "lease-bps", 0, "default bytes-per-second limit per lease (0 = unlimited)")
	flag.Parse()

	flagBootstraps = sdk.ParseURLs(flagBootstrapsCSV)
	if err := runServer(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info().
		Str("frontend_base_url", flagPortalURL).
		Str("subdomain_pattern", flagPortalSubdomainURL).
		Str("bootstrap_uris", strings.Join(flagBootstraps, ",")).
		Msg("[server] frontend configuration")

	cred := sdk.NewCredential()

	serv := portal.NewRelayServer(cred, flagBootstraps)
	if flagMaxLease > 0 {
		serv.SetMaxRelayedPerLease(flagMaxLease)
	}
	if flagLeaseBPS > 0 {
		serv.GetLeaseManager().SetDefaultBPS(int64(flagLeaseBPS))
	}
	serv.Start()
	defer serv.Stop()

	httpSrv := serveHTTP(fmt.Sprintf(":%d", flagPort), serv, cred.ID(), flagBootstraps, stop)

	<-ctx.Done()
	log.Info().Msg("[server] shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if httpSrv != nil {
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("[server] http server shutdown error")
		}
	}

	log.Info().Msg("[server] shutdown complete")
	return nil
}
