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
	"gosuda.org/portal/portal/sni"
)

const (
	defaultHTTPPort       = 4017
	defaultPortalURL      = "http://localhost:4017"
	defaultSNIPort        = ":443"
	defaultKeylessKeyFile = "/etc/portal/keyless/privkey.pem"
)

// flagPortalURL is kept for package-level consumers in other files.
var flagPortalURL string

type relayServerConfig struct {
	AdminPort      int
	AdminSecretKey string
	LeaseBPS       int
	PortalURL      string
	Bootstraps     []string
	SNIPort        string

	KeylessKeyFile  string
	CloudflareToken string
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	cfg := relayServerConfig{}

	portalURL := strings.TrimSuffix(strings.TrimSpace(os.Getenv("PORTAL_URL")), "/")
	if portalURL == "" {
		portalURL = defaultPortalURL
	}
	bootstrapsCSV := strings.TrimSpace(os.Getenv("BOOTSTRAP_URIS"))
	if bootstrapsCSV == "" {
		bootstrapsCSV = defaultBootstrapFrom(portalURL)
	}
	sniPort := strings.TrimSpace(os.Getenv("SNI_PORT"))
	if sniPort == "" {
		sniPort = defaultSNIPort
	}
	keylessKey := strings.TrimSpace(os.Getenv("KEYLESS_KEY_FILE"))
	if keylessKey == "" {
		keylessKey = defaultKeylessKeyFile
	}
	adminSecretKey := strings.TrimSpace(os.Getenv("ADMIN_SECRET_KEY"))
	cloudflareToken := strings.TrimSpace(os.Getenv("CLOUDFLARE_TOKEN"))

	flag.IntVar(&cfg.AdminPort, "adminport", defaultHTTPPort, "Admin/HTTP server port")
	flag.StringVar(&cfg.AdminSecretKey, "admin-secret-key", adminSecretKey, "admin auth secret (env: ADMIN_SECRET_KEY)")
	flag.IntVar(&cfg.LeaseBPS, "lease-bps", 0, "bytes-per-second limit per lease (0=unlimited)")
	flag.StringVar(&cfg.PortalURL, "portal-url", portalURL, "portal base URL (env: PORTAL_URL)")
	flag.StringVar(&bootstrapsCSV, "bootstraps", bootstrapsCSV, "bootstrap URIs, comma-separated (env: BOOTSTRAP_URIS)")
	flag.StringVar(&cfg.SNIPort, "sni-port", sniPort, "SNI router port (env: SNI_PORT)")
	flag.StringVar(&cfg.KeylessKeyFile, "keyless-key-file", keylessKey, "PEM private key path for relay keyless signer (env: KEYLESS_KEY_FILE)")
	flag.StringVar(&cfg.CloudflareToken, "cloudflare-token", cloudflareToken, "Cloudflare DNS API token (Zone:Read + DNS:Edit) (env: CLOUDFLARE_TOKEN)")
	flag.Parse()

	cfg.Bootstraps = parseURLs(bootstrapsCSV)
	flagPortalURL = cfg.PortalURL
	if err := runServer(cfg); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer(cfg relayServerConfig) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info().
		Str("portal_base_url", cfg.PortalURL).
		Strs("bootstrap_uris", cfg.Bootstraps).
		Msg("[server] frontend configuration")

	serv, err := portal.NewRelayServer(ctx, cfg.Bootstraps, cfg.SNIPort, cfg.PortalURL, cfg.KeylessKeyFile, cfg.CloudflareToken)
	if err != nil {
		return fmt.Errorf("create relay server: %w", err)
	}

	frontend := NewFrontend()
	authManager := manager.NewAuthManager(cfg.AdminSecretKey)
	admin := NewAdmin(int64(cfg.LeaseBPS), frontend, authManager)
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
		return fmt.Errorf("start relay server: %w", err)
	}
	defer serv.Stop()

	httpSrv := serveHTTP(fmt.Sprintf(":%d", cfg.AdminPort), serv, admin, frontend, stop)

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
