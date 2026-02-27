package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/utils/sni"
)

var (
	// Server
	flagPort           int
	flagAdminSecretKey string
	flagNoIndex        bool
	flagLeaseBPS       int

	// Portal
	flagPortalURL  string
	flagBootstraps []string

	// TLS
	flagSNIPort string
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	defaultPortalURL := strings.TrimSuffix(os.Getenv("PORTAL_URL"), "/")
	if defaultPortalURL == "" {
		// Prefer explicit scheme for localhost so downstream URL building is unambiguous
		defaultPortalURL = "http://localhost:4017"
	}
	defaultBootstraps := os.Getenv("BOOTSTRAP_URIS")
	if defaultBootstraps == "" {
		defaultBootstraps = defaultBootstrapFrom(defaultPortalURL)
	}
	defaultSNIPort := os.Getenv("SNI_PORT")
	if defaultSNIPort == "" {
		defaultSNIPort = ":443"
	}

	// Server flags
	flag.IntVar(&flagPort, "port", 4017, "HTTP server port")
	flag.StringVar(&flagAdminSecretKey, "admin-secret-key", os.Getenv("ADMIN_SECRET_KEY"), "admin auth secret (env: ADMIN_SECRET_KEY)")
	flag.BoolVar(&flagNoIndex, "noindex", os.Getenv("NOINDEX") == "true", "disallow crawlers (env: NOINDEX)")
	flag.IntVar(&flagLeaseBPS, "lease-bps", 0, "bytes-per-second limit per lease (0=unlimited)")

	// Portal flags
	var flagBootstrapsCSV string
	flag.StringVar(&flagPortalURL, "portal-url", defaultPortalURL, "portal base URL (env: PORTAL_URL)")
	flag.StringVar(&flagBootstrapsCSV, "bootstraps", defaultBootstraps, "bootstrap URIs, comma-separated (env: BOOTSTRAP_URIS)")

	// TLS flags
	flag.StringVar(&flagSNIPort, "sni-port", defaultSNIPort, "SNI router port (env: SNI_PORT)")

	flag.Parse()

	flagBootstraps = parseURLs(flagBootstrapsCSV)
	if err := runServer(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info().
		Str("portal_base_url", flagPortalURL).
		Strs("bootstrap_uris", flagBootstraps).
		Msg("[server] frontend configuration")

	serv := portal.NewRelayServer(
		ctx,
		flagBootstraps,
		flagSNIPort,
		flagPortalURL,
	)

	frontend := NewFrontend()
	authManager := manager.NewAuthManager(flagAdminSecretKey)
	admin := NewAdmin(int64(flagLeaseBPS), frontend, authManager)
	frontend.SetAdmin(admin)

	// Load persisted admin settings (ban list, BPS limits, IP bans)
	admin.LoadSettings(serv)

	// Set up SNI connection callback to route to tunnel backends
	serv.GetSNIRouter().SetConnectionCallback(func(clientConn net.Conn, route *sni.Route) {
		if _, ok := serv.GetLeaseManager().GetLeaseByID(route.LeaseID); !ok {
			log.Warn().
				Str("lease_id", route.LeaseID).
				Str("sni", route.SNI).
				Msg("[SNI] Lease not active; dropping connection and unregistering route")
			serv.GetSNIRouter().UnregisterRouteByLeaseID(route.LeaseID)
			clientConn.Close()
			return
		}

		// Get BPS manager for rate limiting
		bpsManager := admin.GetBPSManager()

		reverseConn, err := serv.GetReverseHub().AcquireForTLS(route.LeaseID, portal.TLSAcquireWait)
		if err != nil {
			log.Warn().
				Err(err).
				Str("lease_id", route.LeaseID).
				Str("sni", route.SNI).
				Msg("[SNI] Reverse tunnel unavailable")
			clientConn.Close()
			return
		}

		// SNI path is reverse-only (NAT-friendly): relay never dials app directly.
		manager.EstablishRelayWithBPS(clientConn, reverseConn.Conn, route.LeaseID, bpsManager)
		reverseConn.Close()
	})

	if err := serv.Start(); err != nil {
		log.Fatal().Err(err).Msg("[server] Failed to start relay server")
	}
	defer serv.Stop()

	httpSrv := serveHTTP(fmt.Sprintf(":%d", flagPort), serv, admin, frontend, flagNoIndex, stop)

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
