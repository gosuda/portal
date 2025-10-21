package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gosuda/relaydns/relaydns"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "relayserver",
	Short: "A lightweight, DNS-driven peer-to-peer proxy layer built on libp2p",
	RunE:  runServer,
}

var (
	flagBootstraps []string

	httpAddr string // unified admin + HTTP proxy (e.g. :8080)
)

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringSliceVar(&flagBootstraps, "bootstrap", nil, "multiaddrs with /p2p/ (supports /dnsaddr/ that resolves to /p2p/)")

	flags.StringVar(&httpAddr, "http", ":8080", "Unified admin UI and HTTP proxy listen address")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const outBoundPort = 4001
	h, err := relaydns.MakeHost(ctx, outBoundPort, true)
	if err != nil {
		return err
	}
	relaydns.ConnectBootstraps(ctx, h, flagBootstraps)

	d, err := relaydns.NewDirector(ctx, h, "/relaydns/http/1.0", "relaydns.backends")
	if err != nil {
		return err
	}

	// Admin UI + per-peer HTTP proxy served here
	go func() {
		if httpAddr == "" {
			return
		}
		mux := http.NewServeMux()
		// Index page
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			type row struct {
				Peer     string
				Name     string
				DNS      string
				LastSeen string
				Link     string
			}
			type page struct {
				NodeID string
				Addrs  []string
				Rows   []row
			}
			rows := make([]row, 0)
			for _, v := range d.Hosts() {
				rows = append(rows, row{
					Peer:     v.Info.Peer,
					Name:     v.Info.Name,
					DNS:      v.Info.DNS,
					LastSeen: time.Since(v.LastSeen).Round(time.Second).String() + " ago",
					Link:     "/peer/" + v.Info.Peer + "/",
				})
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			log.Debug().Int("clients", len(rows)).Msg("render admin index")
			addrs := make([]string, 0)
			for _, a := range h.Addrs() {
				addrs = append(addrs, fmt.Sprintf("%s/p2p/%s", a.String(), h.ID().String()))
			}
			_ = adminIndexTmpl.Execute(w, page{NodeID: h.ID().String(), Addrs: addrs, Rows: rows})
		})
		// Per-peer proxy
		mux.HandleFunc("/peer/", func(w http.ResponseWriter, r *http.Request) {
			p := strings.TrimPrefix(r.URL.Path, "/peer/")
			parts := strings.SplitN(p, "/", 2)
			if len(parts) == 0 || parts[0] == "" {
				http.Error(w, "missing peer id", http.StatusBadRequest)
				return
			}
			peerID := parts[0]
			pathSuffix := "/"
			if len(parts) == 2 {
				pathSuffix = "/" + parts[1]
			}
			d.ProxyHTTP(w, r, peerID, pathSuffix)
		})
		// JSON hosts
		mux.HandleFunc("/hosts", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(d.Hosts())
		})
		// No override endpoint; selection is explicit per-peer via /peer/{id}
		// Health
		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			type info struct {
				Status string   `json:"status"`
				Addrs  []string `json:"multiaddrs"`
			}
			list := make([]string, 0)
			for _, a := range h.Addrs() {
				list = append(list, fmt.Sprintf("%s/p2p/%s", a.String(), h.ID().String()))
			}
			resp := info{Status: "ok", Addrs: list}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		})

		log.Info().Msgf("[server] http (admin+proxy): %s", httpAddr)
		if err := http.ListenAndServe(httpAddr, mux); err != nil {
			log.Error().Err(err).Msg("[server] http error")
			cancel()
		}
	}()

	// graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	cancel()
	time.Sleep(300 * time.Millisecond)
	return nil
}

var adminIndexTmpl = template.Must(template.New("admin-index").Parse(`<!doctype html>
<html lang="ko">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>RelayDNS — Admin</title>
  <style>
    * { box-sizing: border-box }
    body {
      margin: 0;
      background: #f6f7fb;
      color: #111827;
      font-family: system-ui, Segoe UI, Roboto, Helvetica, Arial, sans-serif;
      font-size: 16px;
      line-height: 1.5;
    }
    .wrap { max-width: 960px; margin: 0 auto; padding: 28px 18px }
    header { display:flex; align-items:center; justify-content:space-between; padding: 14px 18px; background:#ffffff; border:1px solid #e5e7eb; border-radius: 10px }
    .brand { font-weight: 700; font-size: 20px }
    .status { color:#059669; font-weight:600 }
    main { margin-top: 18px; display:block }
    .box { background:#ffffff; border:1px solid #e5e7eb; border-radius:10px; padding:16px; margin-bottom:12px }
    .mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 14px; color:#374151; word-break: break-all }
    .title { font-weight:700; margin: 0 0 8px 0; font-size: 18px; color:#374151 }
    .muted { color:#6b7280; font-size: 14px }
    .btn { display:inline-block; background:#2563eb; color:#fff; text-decoration:none; border-radius:8px; padding:10px 14px; font-weight:700; margin-top: 6px }
  </style>
  </head>
<body>
  <div class="wrap">
    <header>
      <div class="brand">RelayDNS Admin</div>
      <div class="status">Active</div>
    </header>
    <main>
      <section class="box">
        <div class="title">Server</div>
        <div class="mono">Peer ID: {{.NodeID}}</div>
        {{if .Addrs}}
          <div class="muted" style="margin-top:6px">Multiaddrs</div>
          <div class="mono">{{range .Addrs}}{{.}}<br/>{{end}}</div>
        {{end}}
        <div class="muted" style="margin-top:6px">Known clients: {{len .Rows}}</div>
      </section>
      {{range .Rows}}
      <section class="box">
        <div class="title">{{if .Name}}{{.Name}}{{else}}(unnamed){{end}}</div>
        {{if .DNS}}<div class="muted">DNS: <span class="mono">{{.DNS}}</span></div>{{end}}
        <div class="muted">Peer</div>
        <div class="mono">{{.Peer}}</div>
        <div class="muted" style="margin-top:6px">Last seen: {{.LastSeen}}</div>
        <a class="btn" href="{{.Link}}">Open</a>
      </section>
      {{else}}
      <section class="box">
        <div class="title">No clients discovered</div>
        <div class="muted">Start a client and ensure bootstraps are configured.</div>
      </section>
      {{end}}
    </main>
  </div>
</body>
</html>`))
