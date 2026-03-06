package main

import (
	"context"
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
	flag.StringVar(&flagServerURL, "server-url", "https://localhost:4017", "relay API URL (https only)")
	flag.IntVar(&flagPort, "port", 8092, "local demo HTTP port")
	flag.StringVar(&flagName, "name", "demo-app", "backend display name")
	flag.StringVar(&flagDesc, "description", "Portal demo connectivity app", "lease description")
	flag.StringVar(&flagTags, "tags", "demo,connectivity,activity,cloud,sun,morning", "comma-separated lease tags")
	flag.StringVar(&flagOwner, "owner", "PortalApp Developer", "lease owner")
	flag.BoolVar(&flagHide, "hide", false, "hide this lease from listings")
	flag.Parse()

	if err := runDemo(); err != nil {
		fmt.Fprintf(os.Stderr, "demo command failed: %v\n", err)
		os.Exit(1)
	}
}

func runDemo() error {
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
		_ = localSrv.ListenAndServe()
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
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
	}

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
