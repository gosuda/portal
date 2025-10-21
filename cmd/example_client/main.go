package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"text/template"
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
	flagServerURL   string
	flagBootstraps  []string
	flagBackendHTTP string
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringVar(&flagServerURL, "server-url", "http://localhost:8080", "relayserver admin base URL to auto-fetch multiaddrs from /health")
	flags.StringSliceVar(&flagBootstraps, "bootstrap", nil, "multiaddrs with /p2p/ (supports /dnsaddr/ that resolves to /p2p/)")
	flags.StringVar(&flagBackendHTTP, "backend-http", ":8081", "local backend HTTP listen address")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runClient(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1) HTTP backend
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			data := struct {
				Now  string
				Host string
				Addr string
			}{
				Now:  time.Now().Format(time.RFC1123),
				Host: r.Host,
				Addr: flagBackendHTTP,
			}
			_ = pageTmpl.Execute(w, data)
		})
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})

		log.Info().Msgf("[client] local backend http %s", flagBackendHTTP)
		if err := http.ListenAndServe(flagBackendHTTP, mux); err != nil {
			log.Error().Err(err).Msg("[client] http backend error")
			cancel()
		}
	}()

	// 2) libp2p host
	h, err := relaydns.MakeHost(ctx, 0, true)
	if err != nil {
		return fmt.Errorf("make host: %w", err)
	}

	client, err := relaydns.NewClient(ctx, h, relaydns.ClientConfig{
		Protocol:       "/relaydns/http/1.0",
		Topic:          "relaydns.backends",
		AdvertiseEvery: 3 * time.Second,
		TargetTCP:      addrToTarget(flagBackendHTTP),

		ServerURL:   flagServerURL,
		Bootstraps:  flagBootstraps,
		HTTPTimeout: 3 * time.Second,
		PreferQUIC:  true,
		PreferLocal: true,
	})
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	defer client.Close()

	if addrs := h.Addrs(); len(addrs) > 0 {
		for _, a := range addrs {
			log.Info().Msgf("[client] host addr: %s/p2p/%s", a.String(), h.ID().String())
		}
	} else {
		log.Info().Msgf("[client] host peer: %s (no listen addrs yet)", h.ID().String())
	}

	// wait for termination
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info().Msg("[client] shutting down")
	time.Sleep(200 * time.Millisecond)
	return nil
}

func addrToTarget(listen string) string {
	if len(listen) > 0 && listen[0] == ':' {
		return "127.0.0.1" + listen
	}
	return listen
}

var pageTmpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
	<meta charset="UTF-8">
	<title>RelayDNS Backend</title>
	<style>
		body { font-family: sans-serif; background: #f9f9f9; padding: 40px; }
		h1 { color: #333; }
		footer { margin-top: 40px; color: #666; font-size: 0.9em; }
		.card { background: white; border-radius: 12px; padding: 24px; box-shadow: 0 2px 6px rgba(0,0,0,0.1); }
	</style>
</head>
<body>
	<div class="card">
		<h1>🚀 RelayDNS Backend</h1>
		<p>This page is served from the backend node.</p>
		<p>Current time: <b>{{.Now}}</b></p>
		<p>Hostname: <b>{{.Host}}</b></p>
	</div>
	<footer>relaydns demo client — served locally at {{.Addr}}</footer>
</body>
</html>`))
