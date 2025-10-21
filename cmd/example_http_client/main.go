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
	flagServerURL  string
	flagBootstraps []string
	flagAddr       string
	flagClientName string
	flagProtocol   string
	flagTopic      string
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringVar(&flagServerURL, "server-url", "http://localhost:8080", "relayserver admin base URL to auto-fetch multiaddrs from /health")
	flags.StringSliceVar(&flagBootstraps, "bootstrap", nil, "multiaddrs with /p2p/ (supports /dnsaddr/ that resolves to /p2p/)")
	flags.StringVar(&flagAddr, "addr", ":8081", "local backend HTTP listen address")
	flags.StringVar(&flagClientName, "name", "", "backend display name shown on server UI")
	flags.StringVar(&flagProtocol, "protocol", "/relaydns/http/1.0", "libp2p protocol id for streams (must match server)")
	flags.StringVar(&flagTopic, "topic", "relaydns.backends", "pubsub topic for backend adverts")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runClient(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if flagClientName == "" {
		hn, err := os.Hostname()
		if err != nil {
			hn = "unknown-" + rand.Text()
		}
		flagClientName = hn
	}

	// 1) HTTP backend
	var clientRef *relaydns.RelayClient
	ln, err := net.Listen("tcp", flagAddr)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to listen")
	}
	log.Info().Msgf("[client] local backend http listening on %s", ln.Addr().String())

	// Serve local backend view in a goroutine
	go serveClientHTTP(ctx, ln, flagClientName, func() string {
		if clientRef == nil {
			return "Starting..."
		}
		return clientRef.ServerStatus()
	}, cancel)

	// 2) libp2p host
	client, err := relaydns.NewClient(ctx, relaydns.ClientConfig{
		Protocol:       flagProtocol,
		Topic:          flagTopic,
		AdvertiseEvery: 3 * time.Second,
		Name:           flagClientName,
		TargetTCP:      relaydns.AddrToTarget(flagAddr),

		ServerURL:   flagServerURL,
		Bootstraps:  flagBootstraps,
		HTTPTimeout: 5 * time.Second,
		PreferQUIC:  true,
		PreferLocal: true,
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
