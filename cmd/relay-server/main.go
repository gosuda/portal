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
	flagBootstraps   []string
	flagALPN         string
	flagPort         int
	flagStaticDir    string
	flagPortalDomain string
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	// Defaults from environment
	defaultStaticDir := os.Getenv("STATIC_DIR")
	if defaultStaticDir == "" {
		defaultStaticDir = "./dist"
	}
	defaultPortalDomain := os.Getenv("PORTAL_DOMAIN")
	if defaultPortalDomain == "" {
		defaultPortalDomain = "localhost"
	}
	var flagBootstrapsCSV string
	flag.StringVar(&flagBootstrapsCSV, "bootstraps", "ws://localhost:4017/relay", "bootstrap addresses (comma-separated)")
	flag.StringVar(&flagALPN, "alpn", "http/1.1", "ALPN identifier for this service")
	flag.IntVar(&flagPort, "port", 4017, "admin UI and HTTP proxy port")
	flag.StringVar(&flagStaticDir, "static-dir", defaultStaticDir, "static files directory for portal frontend (env: STATIC_DIR)")
	flag.StringVar(&flagPortalDomain, "portal-domain", defaultPortalDomain, "portal domain for frontend serving (env: PORTAL_DOMAIN)")

	flag.Parse()

	// Parse bootstrap list
	parts := strings.Split(flagBootstrapsCSV, ",")
	flagBootstraps = make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s != "" {
			flagBootstraps = append(flagBootstraps, s)
		}
	}

	if err := runServer(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Set static directory and portal domain
	staticDir = flagStaticDir
	portalDomain = flagPortalDomain
	log.Info().
		Str("static_dir", staticDir).
		Str("portal_domain", portalDomain).
		Msg("[server] frontend configuration")

	cred := sdk.NewCredential()

	serv := portal.NewRelayServer(cred, flagBootstraps)
	serv.Start()
	defer serv.Stop()

	// Admin UI + Relay + Static Frontend
	httpSrv := serveHTTP(ctx, fmt.Sprintf(":%d", flagPort), serv, cred.ID(), flagBootstraps, stop)

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
