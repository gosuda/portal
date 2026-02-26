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

	"github.com/coder/websocket"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/sdk"
)

//go:embed static
var staticFiles embed.FS

//go:embed static/thumbnail.png
var thumbnailPNG []byte

var (
	flagRelayURL      string
	flagPort          int
	flagName          string
	flagDesc          string
	flagTags          string
	flagOwner         string
	flagHide          bool
	flagFunnelWorkers int
)

func main() {
	flag.StringVar(&flagRelayURL, "relay-url", "http://localhost:4017", "relay HTTP base URL")
	flag.IntVar(&flagPort, "port", 8092, "local demo HTTP port")
	flag.StringVar(&flagName, "name", "demo-app", "backend display name")
	flag.StringVar(&flagDesc, "description", "Portal demo connectivity app", "lease description")
	flag.StringVar(&flagTags, "tags", "demo,connectivity,activity,cloud,sun,moning", "comma-separated lease tags")
	flag.StringVar(&flagOwner, "owner", "PortalApp Developer", "lease owner")
	flag.BoolVar(&flagHide, "hide", false, "hide this lease from listings")
	flag.IntVar(&flagFunnelWorkers, "funnel-workers", 4, "number of reverse connection workers")

	flag.Parse()

	if err := runDemo(); err != nil {
		log.Fatal().Err(err).Msg("execute demo command")
	}
}

// handleWS is a minimal WebSocket echo handler to verify bidirectional connectivity.
func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("accept websocket")
		return
	}
	defer conn.CloseNow()

	ctx := r.Context()
	for {
		msgType, data, err := conn.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				log.Error().Err(err).Msg("read websocket message")
			}
			return
		}
		if err := conn.Write(ctx, msgType, data); err != nil {
			log.Error().Err(err).Msg("write websocket message")
			return
		}
	}
}

// buildMux creates the shared HTTP handler.
func buildMux() (http.Handler, error) {
	mux := http.NewServeMux()

	// Serve static files from embedded filesystem.
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, fmt.Errorf("create static fs: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	// Simple HTTP ping endpoint for connectivity checks.
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

	// WebSocket echo endpoint for bidirectional test.
	mux.HandleFunc("/ws", handleWS)

	// Test endpoint for multiple Set-Cookie headers.
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

	return mux, nil
}

func runDemo() error {
	mux, err := buildMux()
	if err != nil {
		return err
	}

	localAddr := fmt.Sprintf(":%d", flagPort)

	// Local-only mode: skip SDK registration when relay-url is empty.
	if flagRelayURL == "" {
		log.Info().Msgf("[demo] serving on local port %s (local-only mode, no relay)", localAddr)

		srvErr := make(chan error, 1)
		go func() {
			srvErr <- http.ListenAndServe(localAddr, mux)
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

	// Serve on local port for direct testing alongside funnel.
	go func() {
		log.Info().Msgf("[demo] also serving on local port %s for direct testing", localAddr)
		if err := http.ListenAndServe(localAddr, mux); err != nil {
			log.Error().Err(err).Msg("local http serve error")
		}
	}()

	// Register via funnel SDK and serve HTTP over TLS-terminated reverse connections.
	client := sdk.NewFunnelClient(flagRelayURL)

	// Create base64 data URI from embedded thumbnail.
	thumbnailDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(thumbnailPNG)

	var opts []sdk.FunnelOption
	if flagDesc != "" {
		opts = append(opts, sdk.WithFunnelDescription(flagDesc))
	}
	if flagTags != "" {
		opts = append(opts, sdk.WithFunnelTags(strings.Split(flagTags, ",")...))
	}
	opts = append(opts, sdk.WithFunnelThumbnail(thumbnailDataURI))
	if flagOwner != "" {
		opts = append(opts, sdk.WithFunnelOwner(flagOwner))
	}
	if flagHide {
		opts = append(opts, sdk.WithFunnelHide(flagHide))
	}
	if flagFunnelWorkers > 0 {
		opts = append(opts, sdk.WithFunnelWorkers(flagFunnelWorkers))
	}

	listener, err := client.Register(flagName, opts...)
	if err != nil {
		return fmt.Errorf("funnel register: %w", err)
	}
	defer listener.Close()

	log.Info().
		Str("name", flagName).
		Str("public_url", listener.PublicURL()).
		Str("lease_id", listener.LeaseID()).
		Msg("[demo] serving HTTP over funnel (SNI throughpass)")

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
