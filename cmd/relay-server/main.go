package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"gosuda.org/portal/utils"
)

var (
	flagPortalURL      string
	flagPortalAppURL   string
	flagBootstraps     []string
	flagALPN           string
	flagPort           int
	flagMaxLease       int
	flagLeaseBPS       int
	flagNoIndex        bool
	flagAdminSecretKey string
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	defaultPortalURL := strings.TrimSuffix(os.Getenv("PORTAL_URL"), "/")
	if defaultPortalURL == "" {
		// Prefer explicit scheme for localhost so downstream URL building is unambiguous
		defaultPortalURL = "http://localhost:4017"
	}
	defaultAppURL := os.Getenv("PORTAL_APP_URL")
	if defaultAppURL == "" {
		defaultAppURL = utils.DefaultAppPattern(defaultPortalURL)
	}
	defaultBootstraps := os.Getenv("BOOTSTRAP_URIS")
	if defaultBootstraps == "" {
		defaultBootstraps = utils.DefaultBootstrapFrom(defaultPortalURL)
	}

	var flagBootstrapsCSV string
	flag.StringVar(&flagPortalURL, "portal-url", defaultPortalURL, "base URL for portal frontend (env: PORTAL_URL)")
	flag.StringVar(&flagPortalAppURL, "portal-app-url", defaultAppURL, "subdomain wildcard URL (env: PORTAL_APP_URL)")
	flag.StringVar(&flagBootstrapsCSV, "bootstraps", defaultBootstraps, "bootstrap addresses (comma-separated)")
	flag.StringVar(&flagALPN, "alpn", "http/1.1", "ALPN identifier for this service")
	flag.IntVar(&flagPort, "port", 4017, "app UI and HTTP proxy port")
	flag.IntVar(&flagMaxLease, "max-lease", 0, "maximum active relayed connections per lease (0 = unlimited)")
	flag.IntVar(&flagLeaseBPS, "lease-bps", 0, "default bytes-per-second limit per lease (0 = unlimited)")

	defaultNoIndex := os.Getenv("NOINDEX") == "true"
	flag.BoolVar(&flagNoIndex, "noindex", defaultNoIndex, "disallow all crawlers via robots.txt (env: NOINDEX)")

	defaultAdminSecretKey := os.Getenv("ADMIN_SECRET_KEY")
	flag.StringVar(&flagAdminSecretKey, "admin-secret-key", defaultAdminSecretKey, "secret key for admin authentication (env: ADMIN_SECRET_KEY)")
	flag.Parse()

	flagBootstraps = utils.ParseURLs(flagBootstrapsCSV)
	if err := runServer(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info().
		Str("portal_base_url", flagPortalURL).
		Str("app_url", flagPortalAppURL).
		Str("bootstrap_uris", strings.Join(flagBootstraps, ",")).
		Msg("[server] frontend configuration")

	serv := portal.NewRelayServer(flagBootstraps)

	// Create AuthManager for admin authentication
	// Auto-generate secret key if not provided
	if flagAdminSecretKey == "" {
		randomBytes := make([]byte, 16)
		if _, err := rand.Read(randomBytes); err != nil {
			log.Fatal().Err(err).Msg("[server] failed to generate random admin secret key")
		}
		flagAdminSecretKey = hex.EncodeToString(randomBytes)
		log.Warn().Str("key", flagAdminSecretKey).Msg("[server] auto-generated ADMIN_SECRET_KEY (set ADMIN_SECRET_KEY env to use your own)")
	} else {
		log.Info().Str("key", flagAdminSecretKey).Msg("[server] admin authentication enabled")
	}
	authManager := manager.NewAuthManager(flagAdminSecretKey)

	// Create Frontend first, then Admin, then attach Admin back to Frontend.
	frontend := NewFrontend()
	admin := NewAdmin(int64(flagLeaseBPS), frontend, authManager)
	frontend.SetAdmin(admin)

	// Load persisted admin settings (ban list, BPS limits, IP bans)
	admin.LoadSettings(serv)

	// Start SNI-based TCP router for TLS passthrough
	sniRouter := sni.NewRouter()

	// Set up connection callback to route to tunnel backends
	sniRouter.SetConnectionCallback(func(clientConn net.Conn, route *sni.Route) {
		if _, ok := serv.GetLeaseManager().GetLeaseByID(route.LeaseID); !ok {
			log.Warn().
				Str("lease_id", route.LeaseID).
				Str("sni", route.SNI).
				Msg("[SNI] Lease not active; dropping connection and unregistering route")
			sniRouter.UnregisterRouteByLeaseID(route.LeaseID)
			clientConn.Close()
			return
		}

		// Get BPS manager for rate limiting
		bpsManager := admin.GetBPSManager()

		// Prefer reverse tunnel (NAT-friendly) if available.
		if reverseConn, err := serv.GetReverseHub().AcquireStarted(route.LeaseID, portal.ReverseSNIAcquireWait); err == nil {
			defer reverseConn.Close()
			manager.EstablishRelayWithBPS(clientConn, reverseConn.Conn, route.LeaseID, bpsManager)
			return
		}

		// Connect to tunnel backend
		tunnelConn, err := net.DialTimeout("tcp", route.TargetAddr, 10*time.Second)
		if err != nil {
			log.Error().
				Err(err).
				Str("target", route.TargetAddr).
				Str("sni", route.SNI).
				Msg("[SNI] Failed to connect to tunnel backend")
			clientConn.Close()
			return
		}

		// Establish relay with BPS limiting
		manager.EstablishRelayWithBPS(clientConn, tunnelConn, route.LeaseID, bpsManager)
	})

	// Start SNI router on port 443 (or configurable port)
	sniPort := ":443"
	if envPort := os.Getenv("SNI_PORT"); envPort != "" {
		sniPort = envPort
	}

	if err := sniRouter.Start(sniPort); err != nil {
		log.Error().Err(err).Str("port", sniPort).Msg("[server] Failed to start SNI router")
		// Continue without SNI router - HTTP proxy still works
	} else {
		log.Info().Str("port", sniPort).Msg("[server] SNI router started")
		defer sniRouter.Stop()
	}

	serv.Start()
	defer serv.Stop()

	httpSrv := serveHTTP(fmt.Sprintf(":%d", flagPort), serv, sniRouter, admin, frontend, flagNoIndex, stop)

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
