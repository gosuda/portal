package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/sdk"
)

var (
	flagBootstraps []string
	flagALPN       string
	flagPort       int
	flagUDPPort    int
	flagStaticDir  string
	flagPortalHost string
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	// Defaults from environment
	defaultStaticDir := os.Getenv("STATIC_DIR")
	if defaultStaticDir == "" {
		defaultStaticDir = "./dist"
	}
	// Parse PORTAL_UI_URL or POSTAL_FRONTEND_URL to extract portal host
	defaultPortalHost := os.Getenv("PORTAL_UI_URL")
	if defaultPortalHost == "" {
		defaultPortalHost = os.Getenv("POSTAL_FRONTEND_URL")
	}
	if defaultPortalHost != "" {
		// Extract host from URL (supports wildcard patterns like http://*.localhost:4017)
		defaultPortalHost = strings.TrimPrefix(defaultPortalHost, "http://")
		defaultPortalHost = strings.TrimPrefix(defaultPortalHost, "https://")
		defaultPortalHost = strings.TrimPrefix(defaultPortalHost, "*.")
	} else {
		defaultPortalHost = "localhost:4017"
	}
	defaultBootstraps := os.Getenv("BOOTSTRAP_URIS")
	if defaultBootstraps == "" {
		defaultBootstraps = "ws://localhost:4017/relay"
	}
	var flagBootstrapsCSV string
	flag.StringVar(&flagBootstrapsCSV, "bootstraps", defaultBootstraps, "bootstrap addresses (comma-separated)")
	flag.StringVar(&flagALPN, "alpn", "http/1.1", "ALPN identifier for this service")
	flag.IntVar(&flagPort, "port", 4017, "admin UI and HTTP proxy port")
	flag.IntVar(&flagUDPPort, "udp-port", 19132, "UDP relay port (default: 19132 for Minecraft Bedrock)")
	flag.StringVar(&flagStaticDir, "static-dir", defaultStaticDir, "static files directory for portal frontend (env: STATIC_DIR)")
	flag.StringVar(&flagPortalHost, "portal-host", defaultPortalHost, "portal host for frontend serving (env: PORTAL_HOST)")

	flag.Parse()

	// Parse bootstrap list
	parts := strings.Split(flagBootstrapsCSV, ",")
	flagBootstraps = make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s != "" {
			flagBootstraps = append(flagBootstraps, s)
		}
	}

	if err := runServer(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Set static directory and portal host
	staticDir = flagStaticDir
	portalHost = flagPortalHost

	// Set portal UI URL from environment or construct from portal host
	portalUIURL = os.Getenv("PORTAL_UI_URL")
	if portalUIURL == "" {
		portalUIURL = os.Getenv("POSTAL_FRONTEND_URL")
	}
	if portalUIURL == "" {
		portalUIURL = "http://" + portalHost
	}
	// Trim trailing slashes
	portalUIURL = strings.TrimSuffix(portalUIURL, "/")

	// Set portal frontend pattern from POSTAL_FRONTEND_URL
	postalFrontendURL := os.Getenv("POSTAL_FRONTEND_URL")
	if postalFrontendURL != "" {
		// Extract host pattern from URL (e.g., http://*.localhost:4017 -> *.localhost:4017)
		postalFrontendURL = strings.TrimPrefix(postalFrontendURL, "http://")
		postalFrontendURL = strings.TrimPrefix(postalFrontendURL, "https://")
		portalFrontendPattern = postalFrontendURL
	}

	// Set bootstrap URIs from environment
	bootstrapURIs = os.Getenv("BOOTSTRAP_URIS")
	if bootstrapURIs == "" {
		// Use flagBootstraps as fallback
		bootstrapURIs = strings.Join(flagBootstraps, ",")
	}

	log.Info().
		Str("static_dir", staticDir).
		Str("portal_host", portalHost).
		Str("portal_ui_url", portalUIURL).
		Str("portal_frontend_pattern", portalFrontendPattern).
		Str("bootstrap_uris", bootstrapURIs).
		Msg("[server] frontend configuration")

	cred := sdk.NewCredential()

	serv := portal.NewRelayServer(cred, flagBootstraps)
	serv.Start()
	defer serv.Stop()

	// Start UDP relay if port is specified
	if flagUDPPort > 0 {
		udpAddr := fmt.Sprintf(":%d", flagUDPPort)
		udpRelay, err := portal.NewUDPRelay(udpAddr, serv)
		if err != nil {
			log.Fatal().Err(err).Str("addr", udpAddr).Msg("[server] failed to create UDP relay")
		}
		serv.SetUDPRelay(udpRelay)
		if err := udpRelay.Start(); err != nil {
			log.Fatal().Err(err).Msg("[server] failed to start UDP relay")
		}
		log.Info().Int("port", flagUDPPort).Msg("[server] UDP relay started")
	}

	// Admin UI + Relay + Static Frontend
	httpSrv := serveHTTP(ctx, fmt.Sprintf(":%d", flagPort), serv, cred.ID(), flagBootstraps, stop)

	<-ctx.Done()
	log.Info().Msg("[server] shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if httpSrv != nil {
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("[server] http server shutdown error")
		}
	}

	log.Info().Msg("[server] shutdown complete")
	return nil
}
