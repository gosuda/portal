package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/sdk"
)

var rootCmd = &cobra.Command{
	Use:   "portal",
	Short: "A lightweight, DNS-driven peer-to-peer proxy",
	RunE:  runServer,
}

var (
	flagBootstraps   []string
	flagALPN         string
	flagPort         int
	flagStaticDir    string
	flagPortalDomain string
)

func init() {
	// Get default values from environment variables
	defaultStaticDir := os.Getenv("STATIC_DIR")
	if defaultStaticDir == "" {
		defaultStaticDir = "./dist"
	}

	defaultPortalDomain := os.Getenv("PORTAL_DOMAIN")
	if defaultPortalDomain == "" {
		defaultPortalDomain = "portal.gosuda.org"
	}

	flags := rootCmd.PersistentFlags()
	flags.StringArrayVar(&flagBootstraps, "bootstraps", []string{"ws://localhost:4017/relay"}, "bootstrap addresses")
	flags.StringVar(&flagALPN, "alpn", "http/1.1", "ALPN identifier for this service")
	flags.IntVar(&flagPort, "port", 4017, "admin UI and HTTP proxy port")
	flags.StringVar(&flagStaticDir, "static-dir", defaultStaticDir, "static files directory for portal frontend (env: STATIC_DIR)")
	flags.StringVar(&flagPortalDomain, "portal-domain", defaultPortalDomain, "portal domain for frontend serving (env: PORTAL_DOMAIN)")
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer(cmd *cobra.Command, args []string) error {
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
