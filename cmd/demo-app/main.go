package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/gosuda/relaydns/sdk"
)

var (
	flagBootstraps []string
	flagALPNs      []string
	flagName       string
	flagPort       int
)

var rootCmd = &cobra.Command{
	Use:   "relayclient",
	Short: "RelayDNS demo client that serves a simple HTTP backend over the relay",
	RunE:  runClient,
}

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringArrayVar(&flagBootstraps, "bootstrap", []string{"ws://127.0.0.1:4017/relay"}, "bootstrap websocket urls")
	flags.StringArrayVar(&flagALPNs, "alpn", []string{"h1"}, "ALPN identifiers for this service")
	flags.StringVar(&flagName, "name", "demo-app", "lease name to display on server UI")
	flags.IntVar(&flagPort, "port", 4018, "demo app port")
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute client")
	}
}

func runClient(cmd *cobra.Command, args []string) error {
	// Ctrl-C / SIGTERM handling
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Create credential for this client (in-memory)
	cred, err := sdk.NewCredential()
	if err != nil {
		return err
	}

	// Create client and connect to relay(s)
	client, err := sdk.NewClient(func(c *sdk.RDClientConfig) {
		c.BootstrapServers = flagBootstraps
	})
	if err != nil {
		return err
	}
	defer client.Close()

	// Register lease and obtain a net.Listener that accepts relayed connections
	listener, err := client.Listen(cred, flagName, flagALPNs)
	if err != nil {
		return err
	}
	defer listener.Close()

	// Simple HTTP backend on the relay listener
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok: relaydns backend\n")
		fmt.Fprintf(w, "time: %s\n", time.Now().Format(time.RFC3339))
		fmt.Fprintf(w, "method: %s\n", r.Method)
		fmt.Fprintf(w, "path: %s\n", r.URL.Path)
		fmt.Fprintf(w, "client: %s\n", r.RemoteAddr)
		fmt.Fprintf(w, "server: %s\n", r.Host)
	})

	// Optional local admin UI
	var adminSrv *http.Server
	if flagPort > 0 {
		adminSrv = serveClientHTTP(ctx, fmt.Sprintf(":%d", flagPort), cred.ID(), flagName, flagALPNs, flagBootstraps, client.GetRelays, stop)
	}

	// Serve HTTP over relay listener
	srvErr := make(chan error, 1)
	go func() {
		log.Info().Msgf("[client] serving HTTP over relay; lease=%s id=%s", flagName, cred.ID())
		srvErr <- http.Serve(listener, mux)
	}()

	select {
	case <-ctx.Done():
		log.Info().Msg("[client] shutting down...")
	case err := <-srvErr:
		if err != nil {
			log.Error().Err(err).Msg("[client] http serve error")
		}
	}

	// Stop admin UI if started
	if adminSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = adminSrv.Shutdown(shutdownCtx)
	}

	log.Info().Msg("[client] shutdown complete")
	return nil
}
