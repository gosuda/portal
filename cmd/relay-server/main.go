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

	"github.com/gosuda/relaydns/relaydns"
	"github.com/gosuda/relaydns/sdk"
)

var rootCmd = &cobra.Command{
	Use:   "relayserver",
	Short: "A lightweight, DNS-driven peer-to-peer proxy",
	RunE:  runServer,
}

var (
	flagBootstraps []string
	flagALPN       string
	flagPort       int
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringArrayVar(&flagBootstraps, "bootstraps", nil, "bootstrap addresses")
	flags.StringVar(&flagALPN, "alpn", "http/1.1", "ALPN identifier for this service")
	flags.IntVar(&flagPort, "port", 4017, "admin UI and HTTP proxy port")
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cred := sdk.NewCredential()

	serv := relaydns.NewRelayServer(cred, flagBootstraps)
	serv.Start()
	defer serv.Stop()

	// Admin UI + per-peer HTTP proxy
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
