package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/sdk"
)

//go:embed static
var staticFiles embed.FS

//go:embed static/thumbnail.png
var thumbnailPNG []byte

type demoConfig struct {
	ServerURL   string
	Port        int
	Name        string
	Description string
	Tags        string
	Owner       string
	Hide        bool
}

func main() {
	var cfg demoConfig
	flag.StringVar(&cfg.ServerURL, "server-url", "https://localhost:4017/relay", "relay server URL")
	flag.IntVar(&cfg.Port, "port", 8092, "local demo HTTP port")
	flag.StringVar(&cfg.Name, "name", "demo-app", "backend display name")
	flag.StringVar(&cfg.Description, "description", "Portal demo connectivity app", "lease description")
	flag.StringVar(&cfg.Tags, "tags", "demo,connectivity,activity,cloud,sun,morning", "comma-separated lease tags")
	flag.StringVar(&cfg.Owner, "owner", "PortalApp Developer", "lease owner")
	flag.BoolVar(&cfg.Hide, "hide", false, "hide this lease from listings")

	flag.Parse()

	if err := runDemo(cfg); err != nil {
		log.Fatal().Err(err).Msg("execute demo command")
	}
}

// handleWS is a minimal WebSocket echo handler to verify bidirectional connectivity.
func handleWS(w http.ResponseWriter, r *http.Request) {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(_ *http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("upgrade websocket")
		return
	}
	defer conn.Close()

	for {
		messageType, data, readErr := conn.ReadMessage()
		if readErr != nil {
			if websocket.IsUnexpectedCloseError(readErr, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Error().Err(readErr).Msg("read websocket message")
			}
			break
		}

		writeErr := conn.WriteMessage(messageType, data)
		if writeErr != nil {
			log.Error().Err(writeErr).Msg("write websocket message")
			break
		}
	}
}

func runDemo(cfg demoConfig) error {
	// 1) Create credential for this demo app
	cred := sdk.NewCredential()

	// 2) Create SDK client and connect to relay(s)
	client, err := sdk.NewClient(func(c *sdk.ClientConfig) {
		c.BootstrapServers = []string{cfg.ServerURL}
	})
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	defer client.Close()

	// 3) Register lease
	// Create base64 data URI from embedded thumbnail
	thumbnailDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(thumbnailPNG)

	listener, err := client.Listen(
		cred,
		cfg.Name,
		[]string{"http/1.1"},
		sdk.WithDescription(cfg.Description),
		sdk.WithTags(strings.Split(cfg.Tags, ",")),
		sdk.WithOwner(cfg.Owner),
		sdk.WithThumbnail(thumbnailDataURI),
		sdk.WithHide(cfg.Hide),
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
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"message": "pong",
			"time":    time.Now().UTC().Format(time.RFC3339),
		}
		encodeErr := json.NewEncoder(w).Encode(resp)
		if encodeErr != nil {
			log.Error().Err(encodeErr).Msg("write ping response")
		}
	})

	// WebSocket echo endpoint for bidirectional test
	mux.HandleFunc("/ws", handleWS)

	// Test endpoint for multiple Set-Cookie headers
	mux.HandleFunc("/api/test-cookies", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     "session_id",
			Value:    "abc123",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   3600,
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
		encodeErr := json.NewEncoder(w).Encode(map[string]any{
			"message": "3 cookies set: session_id, csrf_token, user_pref",
		})
		if encodeErr != nil {
			log.Error().Err(encodeErr).Msg("write cookie test response")
		}
	})

	// 5) Serve HTTP over relay listener
	log.Info().Msgf("[demo] serving HTTP over relay; lease=%s id=%s", cfg.Name, cred.ID())

	// Also serve on local port for direct testing
	go func() {
		localAddr := fmt.Sprintf(":%d", cfg.Port)
		log.Info().Msgf("[demo] also serving on local port %s for direct testing", localAddr)
		localSrv := &http.Server{
			Addr:              localAddr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		}
		serveErr := localSrv.ListenAndServe()
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Error().Err(serveErr).Msg("local http serve error")
		}
	}()

	relaySrv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	srvErr := make(chan error, 1)
	go func() {
		srvErr <- relaySrv.Serve(listener)
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sig:
		log.Info().Msg("[demo] shutting down...")
	case serveErr := <-srvErr:
		if serveErr != nil {
			log.Error().Err(serveErr).Msg("[demo] http serve error")
		}
	}

	log.Info().Msg("[demo] shutdown complete")
	return nil
}
