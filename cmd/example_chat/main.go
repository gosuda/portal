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
	flagServerURL  string
	flagBootstraps []string
	flagAddr       string
	flagName       string
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringVar(&flagServerURL, "server-url", "http://localhost:8080", "relayserver base URL to auto-fetch multiaddrs from /health")
	flags.StringSliceVar(&flagBootstraps, "bootstrap", nil, "multiaddrs with /p2p/ (supports /dnsaddr/ that resolves to /p2p/)")
	flags.StringVar(&flagAddr, "addr", ":8091", "local chat HTTP listen address")
	flags.StringVar(&flagName, "name", "demo-chat", "backend display name")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute chat command")
	}
}

func runChat(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1) start local chat HTTP backend
	ln, err := net.Listen("tcp", flagAddr)
	if err != nil {
		return fmt.Errorf("listen chat: %w", err)
	}
	hub := newHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { serveIndex(w, r, flagName) })
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) { handleWS(w, r, hub) })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		log.Info().Msgf("[chat] http listening on %s", ln.Addr().String())
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("chat http error")
			cancel()
		}
	}()

	// 2) advertise over RelayDNS (HTTP tunneled via server /peer route)
	client, err := relaydns.NewClient(ctx, relaydns.ClientConfig{
		Protocol:       "/relaydns/http/1.0",
		Topic:          "relaydns.backends",
		AdvertiseEvery: 3 * time.Second,
		Name:           flagName,
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
	defer client.Close()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info().Msg("[chat] shutting down")
	shutCtx, cancelFn := context.WithTimeout(context.Background(), 2*time.Second)
	_ = srv.Shutdown(shutCtx)
	cancelFn()
	return nil
}
