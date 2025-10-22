package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/gosuda/relaydns/sdk/go"
)

var rootCmd = &cobra.Command{
	Use:   "relaydns-client",
	Short: "RelayDNS demo client (local HTTP backend + libp2p advertiser)",
	RunE:  runClient,
}

var (
	flagServerURL string
	flagPort      int
	flagName      string
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringVar(&flagServerURL, "server-url", "http://relaydns.gosuda.org", "relayserver admin base URL to auto-fetch multiaddrs from /health")
	flags.IntVar(&flagPort, "port", 8081, "local backend HTTP port")
	flags.StringVar(&flagName, "name", "example-backend", "backend display name shown on server UI")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runClient(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure context is cancelled on all exit paths

	// 1) HTTP backend
	var clientRef *sdk.RelayClient
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", flagPort))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to listen")
	}
	log.Info().Msgf("[client] local backend http listening on %s", ln.Addr().String())

	// Serve local backend view and keep a server handle for shutdown
	srv := serveClientHTTP(ln, flagName, func() string {
		if clientRef == nil {
			return "Starting..."
		}
		return clientRef.ServerStatus()
	})

	// 2) libp2p host
	rc, err := sdk.NewClient(ctx, sdk.ClientConfig{
		Name:      flagName,
		TargetTCP: fmt.Sprintf("127.0.0.1:%d", flagPort),
		ServerURL: flagServerURL,
	})
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	if err := rc.Start(ctx); err != nil {
		return fmt.Errorf("start client: %w", err)
	}
	clientRef = rc

	// wait for termination
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info().Msg("[client] shutting down...")

	// Shutdown sequence:
	// Note: defer cancel() at function start stops client advertising/refresh loops

	// 1. Close client (waits for goroutines, closes libp2p host)
	if err := rc.Close(); err != nil {
		log.Warn().Err(err).Msg("[client] client close error")
	}

	// 2. Shutdown HTTP server with a fresh context (with timeout)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("[client] http server shutdown error")
	}

	log.Info().Msg("[client] shutdown complete")
	return nil
}
