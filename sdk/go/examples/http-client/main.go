package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	sdk "github.com/gosuda/relaydns/sdk/go"
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

	// 1) start local HTTP backend
	var relayClient *sdk.RelayClient
	httpLn, err := net.Listen("tcp", fmt.Sprintf(":%d", flagPort))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to listen")
	}
	log.Info().Msgf("[client] local backend http listening on %s", httpLn.Addr().String())
	handler := NewHandler(httpLn.Addr().String(), flagName, func() string {
		if relayClient == nil {
			return "Starting..."
		}
		return relayClient.ServerStatus()
	})
	httpSrv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		if err := httpSrv.Serve(httpLn); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg(" http backend error")
		}
	}()

	// 2) libp2p client
	client, err := sdk.NewClient(ctx, sdk.ClientConfig{
		Name:      flagName,
		TargetTCP: fmt.Sprintf("127.0.0.1:%d", flagPort),
		ServerURL: flagServerURL,
	})
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start client: %w", err)
	}
	relayClient = client

	// wait for termination
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info().Msg("[client] shutting down...")

	// Shutdown sequence:
	// 1. Close client (waits for goroutines, closes libp2p host)
	if err := client.Close(); err != nil {
		log.Warn().Err(err).Msg("[client] client close error")
	}

	// 2. Shutdown HTTP server with a fresh context (with timeout)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("[client] http server shutdown error")
	}

	log.Info().Msg("[client] shutdown complete")
	return nil
}
