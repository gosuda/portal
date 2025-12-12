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

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/sdk"
	"gosuda.org/portal/utils"
)

var (
	flagPortalURL    string
	flagPortalAppURL string
	flagBootstraps   []string
	flagALPN         string
	flagPort         int
	flagMaxLease     int
	flagLeaseBPS     int
	flagNoIndex      bool
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	defaultPortalURL := strings.TrimSuffix(os.Getenv("PORTAL_URL"), "/")
	if defaultPortalURL == "" {
		// Prefer explicit scheme for localhost so downstream URL building is unambiguous
		defaultPortalURL = "http://localhost:4017"
	}
	defaultAppURL := os.Getenv("PORTAL_APP_URL")
	if defaultAppURL == "" {
		defaultAppURL = utils.DefaultAppPattern(defaultPortalURL)
	}
	defaultBootstraps := os.Getenv("BOOTSTRAP_URIS")
	if defaultBootstraps == "" {
		defaultBootstraps = utils.DefaultBootstrapFrom(defaultPortalURL)
	}

	var flagBootstrapsCSV string
	flag.StringVar(&flagPortalURL, "portal-url", defaultPortalURL, "base URL for portal frontend (env: PORTAL_URL)")
	flag.StringVar(&flagPortalAppURL, "portal-app-url", defaultAppURL, "subdomain wildcard URL (env: PORTAL_APP_URL)")
	flag.StringVar(&flagBootstrapsCSV, "bootstraps", defaultBootstraps, "bootstrap addresses (comma-separated)")
	flag.StringVar(&flagALPN, "alpn", "http/1.1", "ALPN identifier for this service")
	flag.IntVar(&flagPort, "port", 4017, "app UI and HTTP proxy port")
	flag.IntVar(&flagMaxLease, "max-lease", 0, "maximum active relayed connections per lease (0 = unlimited)")
	flag.IntVar(&flagLeaseBPS, "lease-bps", 0, "default bytes-per-second limit per lease (0 = unlimited)")

	defaultNoIndex := os.Getenv("NOINDEX") == "true"
	flag.BoolVar(&flagNoIndex, "noindex", defaultNoIndex, "disallow all crawlers via robots.txt (env: NOINDEX)")
	flag.Parse()

	flagBootstraps = utils.ParseURLs(flagBootstrapsCSV)
	if err := runServer(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info().
		Str("portal_base_url", flagPortalURL).
		Str("app_url", flagPortalAppURL).
		Str("bootstrap_uris", strings.Join(flagBootstraps, ",")).
		Msg("[server] frontend configuration")

	cred := sdk.NewCredential()

	serv := portal.NewRelayServer(cred, flagBootstraps)
	if flagMaxLease > 0 {
		serv.SetMaxRelayedPerLease(flagMaxLease)
	}

	// Create BPS manager for rate limiting
	bpsManager := NewBPSManager()
	if flagLeaseBPS > 0 {
		bpsManager.SetDefaultBPS(int64(flagLeaseBPS))
	}

	// Create IP manager for IP-based bans
	ipManager := NewIPManager()
	globalIPManager = ipManager

	// Load persisted admin settings (ban list, BPS limits, IP bans)
	loadAdminSettings(serv, bpsManager, ipManager)

	// Register relay callback for BPS handling and IP tracking
	serv.SetEstablishRelayCallback(func(clientStream, leaseStream *yamux.Stream, leaseID string) {
		// Associate pending IP with this lease
		if ip := popPendingIP(); ip != "" && globalIPManager != nil {
			globalIPManager.RegisterLeaseIP(leaseID, ip)
		}
		establishRelayWithBPS(clientStream, leaseStream, leaseID, bpsManager)
	})

	serv.Start()
	defer serv.Stop()

	httpSrv := serveHTTP(fmt.Sprintf(":%d", flagPort), serv, bpsManager, cred.ID(), flagBootstraps, flagNoIndex, stop)

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
