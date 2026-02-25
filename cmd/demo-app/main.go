package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/websocket"

	"gosuda.org/portal/sdk"
)

//go:embed static
var staticFiles embed.FS

//go:embed static/thumbnail.png
var thumbnailPNG []byte

var (
	flagServerURL string
	flagPort      int
	flagName      string
	flagDesc      string
	flagTags      string
	flagOwner     string
	flagHide      bool
	flagTLS       bool
)

func main() {
	flag.StringVar(&flagServerURL, "server-url", "http://localhost:4017", "relay API URL (http/https)")
	flag.IntVar(&flagPort, "port", 8092, "local demo HTTP port")
	flag.StringVar(&flagName, "name", "demo-app", "backend display name")
	flag.StringVar(&flagDesc, "description", "Portal demo connectivity app", "lease description")
	flag.StringVar(&flagTags, "tags", "demo,connectivity,activity,cloud,sun,moning", "comma-separated lease tags")
	flag.StringVar(&flagOwner, "owner", "PortalApp Developer", "lease owner")
	flag.BoolVar(&flagHide, "hide", false, "hide this lease from listings")
	flag.BoolVar(&flagTLS, "tls", true, "enable TLS (end-to-end encryption via ACME DNS-01)")

	flag.Parse()

	if err := runDemo(); err != nil {
		log.Fatal().Err(err).Msg("execute demo command")
	}
}

func runDemo() error {
	// 1) Create SDK client and connect to relay(s)
	opts := []sdk.ClientOption{sdk.WithBootstrapServers([]string{flagServerURL})}
	if flagTLS {
		opts = append(opts, sdk.WithTLS())
	}
	client, err := sdk.NewClient(opts...)
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	defer client.Close()

	// 2) Register lease
	// Create base64 data URI from embedded thumbnail
	thumbnailDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(thumbnailPNG)

	listener, err := client.Listen(
		flagName,
		sdk.WithDescription(flagDesc),
		sdk.WithTags(strings.Split(flagTags, ",")),
		sdk.WithOwner(flagOwner),
		sdk.WithThumbnail(thumbnailDataURI),
		sdk.WithHide(flagHide),
	)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	// 4) Setup HTTP handler
	mux := http.NewServeMux()

	// Serve static files from embedded filesystem
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("create static fs: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	// Simple HTTP ping endpoint for connectivity checks
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"message": "pong",
			"time":    time.Now().UTC().Format(time.RFC3339),
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Error().Err(err).Msg("write ping response")
		}
	})

	// WebSocket echo endpoint
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			var msg string
			if err := websocket.Message.Receive(conn, &msg); err != nil {
				if err.Error() != "EOF" {
					log.Error().Err(err).Msg("websocket read error")
				}
				break
			}
			log.Debug().Str("msg", msg).Msg("websocket received")
			if err := websocket.Message.Send(conn, "echo: "+msg); err != nil {
				log.Error().Err(err).Msg("websocket write error")
				break
			}
		}
	}))

	// Test endpoint for multiple Set-Cookie headers
	// Note: HttpOnly cookies cannot be set via Service Worker (browser security limitation)
	mux.HandleFunc("/api/test-cookies", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:   "session_id",
			Value:  "abc123",
			Path:   "/",
			MaxAge: 3600,
		})
		http.SetCookie(w, &http.Cookie{
			Name:   "auth_token",
			Value:  "secret456",
			Path:   "/",
			MaxAge: 3600,
		})
		http.SetCookie(w, &http.Cookie{
			Name:   "csrf_token",
			Value:  "xyz789",
			Path:   "/",
			MaxAge: 3600,
		})
		http.SetCookie(w, &http.Cookie{
			Name:   "user_pref",
			Value:  "dark_mode",
			Path:   "/",
			MaxAge: 86400,
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"message": "4 cookies set: session_id, auth_token, csrf_token, user_pref",
		})
	})

	// 5) Serve HTTP over relay listener
	log.Info().Msgf("[demo] serving HTTP over relay; lease=%s", flagName)

	// Also serve on local port for direct testing
	go func() {
		localAddr := fmt.Sprintf(":%d", flagPort)
		log.Info().Msgf("[demo] also serving on local port %s for direct testing", localAddr)
		if err := http.ListenAndServe(localAddr, mux); err != nil {
			log.Error().Err(err).Msg("local http serve error")
		}
	}()

	srvErr := make(chan error, 1)
	go func() {
		srvErr <- http.Serve(listener, mux)
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sig:
		log.Info().Msg("[demo] shutting down...")
	case err := <-srvErr:
		if err != nil {
			log.Error().Err(err).Msg("[demo] http serve error")
		}
	}

	log.Info().Msg("[demo] shutdown complete")
	return nil
}
