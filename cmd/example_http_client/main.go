package main

import (
	"context"
	"crypto/rand"
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
	flags.StringVar(&flagServerURL, "server-url", "http://localhost:8080", "relayserver admin base URL to auto-fetch multiaddrs from /health")
	flags.IntVar(&flagPort, "port", 8081, "local backend HTTP port")
	flags.StringVar(&flagName, "name", "", "backend display name shown on server UI")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runClient(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if flagName == "" {
		hn, err := os.Hostname()
		if err != nil {
			hn = "unknown-" + rand.Text()
		}
		flagName = hn
	}

	// 1) HTTP backend
	var clientRef *relaydns.RelayClient
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", flagPort))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to listen")
	}
	log.Info().Msgf("[client] local backend http listening on %s", ln.Addr().String())

	// Serve local backend view in a goroutine
	go serveClientHTTP(ctx, ln, flagName, func() string {
		if clientRef == nil {
			return "Starting..."
		}
		return clientRef.ServerStatus()
	}, cancel)

	// 2) libp2p host
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
	clientRef = client
	defer client.Close()

	// wait for termination
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info().Msg("[client] shutting down")
	time.Sleep(200 * time.Millisecond)
	return nil
}
