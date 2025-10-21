package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
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
	flagServerURL  string
	flagBootstraps []string
	flagAddr       string
	flagClientName string
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringVar(&flagServerURL, "server-url", "http://localhost:8080", "relayserver admin base URL to auto-fetch multiaddrs from /health")
	flags.StringSliceVar(&flagBootstraps, "bootstrap", nil, "multiaddrs with /p2p/ (supports /dnsaddr/ that resolves to /p2p/)")
	flags.StringVar(&flagAddr, "addr", ":8081", "local backend HTTP listen address")
	flags.StringVar(&flagClientName, "name", "", "backend display name shown on server UI")
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
	ln, err := net.Listen("tcp", flagAddr)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to listen")
	}
	log.Info().Msgf("[client] local backend http listening on %s", ln.Addr().String())

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
				Addr: flagAddr,
			}
			_ = pageTmpl.Execute(w, data)
		})
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})

		log.Info().Msgf("[client] local backend http %s", flagAddr)
		if err := http.Serve(ln, mux); err != nil {
			log.Error().Err(err).Msg("[client] http backend error")
			cancel()
		}
	}()

	// 2) libp2p host
	client, err := relaydns.NewClient(ctx, relaydns.ClientConfig{
		Protocol:       "/relaydns/http/1.0",
		Topic:          "relaydns.backends",
		AdvertiseEvery: 3 * time.Second,
		Name:           flagClientName,
		TargetTCP:      addrToTarget(flagAddr),

		ServerURL:   flagServerURL,
		Bootstraps:  flagBootstraps,
		HTTPTimeout: 3 * time.Second,
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
