package main

import (
	"embed"
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

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/sdk"
	"gosuda.org/portal/utils"
)

//go:embed static
var staticFiles embed.FS

var (
	flagServerURL  string
	flagPort       int
	flagName       string
	flagDesc       string
	flagTags       string
	flagOwner      string
	flagThumbnail  string
	flagHide       bool
)

func main() {
	flag.StringVar(&flagServerURL, "server-url", "ws://localhost:4017/relay", "relay websocket URL")
	flag.IntVar(&flagPort, "port", 8092, "local demo HTTP port")
	flag.StringVar(&flagName, "name", "demo-app", "backend display name")
	flag.StringVar(&flagDesc, "description", "Portal demo connectivity app", "lease description")
	flag.StringVar(&flagTags, "tags", "demo,connectivity,activity,cloud,sun,moning", "comma-separated lease tags")
	flag.StringVar(&flagOwner, "owner", "PortalApp Developer", "lease owner")
	flag.StringVar(&flagThumbnail, "thumbnail", "https://naverpa-phinf.pstatic.net/MjAyNjAxMTRfMjEy/MDAxNzY4MzcxNzc1NTYz.nE942d-5NNcGgRlFBukeLEL9s8fESvMaKL1Uv8eiyNAg.k6gjFLfHcvL6pzWqrQhEPf-JgGCYyEluGv-2fZA2b4Qg.PNG/%EB%84%A4%EC%9D%B4%EB%B2%84_GFA-PC%EB%84%A4%EC%9D%B4%ED%8B%B0%EB%B8%8C-342_228_17683717755352279343803638606717.png", "lease thumbnail URL")
	flag.BoolVar(&flagHide, "hide", false, "hide this lease from listings")

	flag.Parse()

	if err := runDemo(); err != nil {
		log.Fatal().Err(err).Msg("execute demo command")
	}
}

// handleWS is a minimal WebSocket echo handler to verify bidirectional connectivity.
func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := utils.UpgradeWebSocket(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("upgrade websocket")
		return
	}
	defer conn.Close()

	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Error().Err(err).Msg("read websocket message")
			}
			break
		}

		if err := conn.WriteMessage(messageType, data); err != nil {
			log.Error().Err(err).Msg("write websocket message")
			break
		}
	}
}

func runDemo() error {
	// 1) Create credential for this demo app
	cred := sdk.NewCredential()

	// 2) Create SDK client and connect to relay(s)
	client, err := sdk.NewClient(func(c *sdk.ClientConfig) {
		c.BootstrapServers = []string{flagServerURL}
	})
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	defer client.Close()

	// 3) Register lease
	listener, err := client.Listen(
		cred,
		flagName,
		[]string{"http/1.1"},
		sdk.WithDescription(flagDesc),
		sdk.WithTags(strings.Split(flagTags, ",")),
		sdk.WithOwner(flagOwner),
		sdk.WithThumbnail(flagThumbnail),
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

	// WebSocket echo endpoint for bidirectional test
	mux.HandleFunc("/ws", handleWS)

	// Test endpoint for multiple Set-Cookie headers
	mux.HandleFunc("/api/test-cookies", func(w http.ResponseWriter, r *http.Request) {
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
		json.NewEncoder(w).Encode(map[string]any{
			"message": "3 cookies set: session_id, csrf_token, user_pref",
		})
	})

	// 5) Serve HTTP over relay listener
	log.Info().Msgf("[demo] serving HTTP over relay; lease=%s id=%s", flagName, cred.ID())

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
