package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gosuda/relaydns/relaydns"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "relaydns-chat",
	Short: "RelayDNS demo chat (local HTTP backend + libp2p advertiser)",
	RunE:  runChat,
}

var (
	flagServerURL string
	flagPort      int
	flagName      string
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringVar(&flagServerURL, "server-url", "http://relaydns.gosuda.org", "relayserver base URL to auto-fetch multiaddrs from /health")
	flags.IntVar(&flagPort, "port", 8091, "local chat HTTP port")
	flags.StringVar(&flagName, "name", "bangjang-chat", "backend display name")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute chat command")
	}
}

func runChat(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure context is cancelled on all exit paths

	// 1) start local chat HTTP backend
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", flagPort))
	if err != nil {
		return fmt.Errorf("listen chat: %w", err)
	}
	hub := newHub()
	srv := serveChatHTTP(ln, flagName, hub)

	// 2) advertise over RelayDNS (HTTP tunneled via server /peer route)
	client, err := relaydns.NewClient(ctx, relaydns.ClientConfig{
		Name:      flagName,
		TargetTCP: relaydns.AddrToTarget(fmt.Sprintf(":%d", flagPort)),
		ServerURL: flagServerURL,

		Protocol: relaydns.DefaultProtocol,
		Topic:    relaydns.DefaultTopic,
	})
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start client: %w", err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info().Msg("[chat] shutting down...")

	// Shutdown sequence:
	// Note: defer cancel() at function start stops client advertising/refresh loops

	// 1. Close client (waits for goroutines, closes libp2p host)
	if err := client.Close(); err != nil {
		log.Warn().Err(err).Msg("[chat] client close error")
	}

	// 2. Shutdown HTTP server with a fresh context (with timeout)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("[chat] http server shutdown error")
	}

	// 3. Close all websocket connections and wait for handlers to finish
	hub.closeAll()
	hub.wait()

	log.Info().Msg("[chat] shutdown complete")
	return nil
}
