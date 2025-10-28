package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/gosuda/relaydns/relaydns"
	"github.com/gosuda/relaydns/relaydns/core/cryptoops"
)

var rootCmd = &cobra.Command{
	Use:   "relayserver",
	Short: "A lightweight, DNS-driven peer-to-peer proxy",
	RunE:  runServer,
}

var (
	flagPort   int // admin UI + HTTP proxy port (e.g. 4017)
	bootstraps []string
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.IntVar(&flagPort, "port", 4017, "admin UI and HTTP proxy port")
	flags.StringArrayVar(&bootstraps, "bootstraps", nil, "bootstrap addresses")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cred, err := cryptoops.NewCredential()
	if err != nil {
		return err
	}

	serv := relaydns.NewRelayServer(cred, bootstraps)
	serv.Start()
	defer serv.Stop()

	// Admin UI + per-peer HTTP proxy
	httpSrv := serveHTTP(ctx, fmt.Sprintf(":%d", flagPort), serv, cred.ID(), bootstraps, stop)

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
