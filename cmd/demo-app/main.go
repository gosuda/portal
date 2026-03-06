package main

import (
	"context"
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

	"github.com/rs/zerolog"
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
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	logger := log.With().Str("component", "demo-app").Logger()

	flag.StringVar(&flagServerURL, "server-url", "https://gosunuts.xyz", "relay API URL (https only)")
	flag.IntVar(&flagPort, "port", 8092, "local demo HTTP port")
	flag.StringVar(&flagName, "name", "demo-app", "backend display name")
	flag.StringVar(&flagDesc, "description", "Portal demo connectivity app", "lease description")
	flag.StringVar(&flagTags, "tags", "demo,connectivity,activity,cloud,sun,morning", "comma-separated lease tags")
	flag.StringVar(&flagOwner, "owner", "PortalApp Developer", "lease owner")
	flag.BoolVar(&flagHide, "hide", false, "hide this lease from listings")
	flag.Parse()

	if err := runDemo(); err != nil {
		logger.Error().Err(err).Msg("demo command failed")
		os.Exit(1)
	}
}

func runDemo() error {
	logger := log.With().Str("component", "demo-app").Logger()

	sdkClient, err := sdk.NewClient(sdk.ClientConfig{RelayURL: flagServerURL})
	if err != nil {
		return fmt.Errorf("new client: %w", err)
	}
	defer sdkClient.Close()

	thumbnailDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(thumbnailPNG)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := sdkClient.Listen(ctx, sdk.ListenRequest{
		Name: flagName,
		Metadata: sdk.LeaseMetadata{
			Description: flagDesc,
			Tags:        splitCSV(flagTags),
			Owner:       flagOwner,
			Thumbnail:   thumbnailDataURI,
			Hide:        flagHide,
		},
	})
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()

	logger.Info().
		Str("lease_id", listener.LeaseID()).
		Strs("public_urls", listener.PublicURLs()).
		Int("local_port", flagPort).
		Msg("demo app registered with relay")

	mux := http.NewServeMux()

	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("create static fs: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"message": "pong",
			"time":    time.Now().UTC().Format(time.RFC3339),
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			var msg string
			if err := websocket.Message.Receive(conn, &msg); err != nil {
				break
			}
			if err := websocket.Message.Send(conn, "echo: "+msg); err != nil {
				break
			}
		}
	}))

	mux.HandleFunc("/api/test-cookies", func(w http.ResponseWriter, _ *http.Request) {
		for _, cookie := range []*http.Cookie{
			{Name: "session_id", Value: "abc123", Path: "/", MaxAge: 3600},
			{Name: "auth_token", Value: "secret456", Path: "/", MaxAge: 3600},
			{Name: "csrf_token", Value: "xyz789", Path: "/", MaxAge: 3600},
			{Name: "user_pref", Value: "dark_mode", Path: "/", MaxAge: 86400},
		} {
			http.SetCookie(w, cookie)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "4 cookies set: session_id, auth_token, csrf_token, user_pref",
		})
	})

	localAddr := fmt.Sprintf(":%d", flagPort)
	go func() {
		localSrv := &http.Server{
			Addr:              localAddr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		logger.Info().Str("addr", localAddr).Msg("demo app listening locally")
		if err := localSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error().Err(err).Str("addr", localAddr).Msg("demo local server stopped")
		}
	}()

	relaySrv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		errCh <- relaySrv.Serve(listener)
	}()

	select {
	case <-sig:
		logger.Info().Msg("demo app shutting down")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}

	logger.Info().Msg("demo app shutdown complete")
	return nil
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
