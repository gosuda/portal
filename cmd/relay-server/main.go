package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/sdk"
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
	flagTLSCert        string
	flagTLSKey         string
	flagTLSAuto        bool
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

	flag.StringVar(&flagTLSCert, "tls-cert", "", "TLS certificate file path (required for WebTransport)")
	flag.StringVar(&flagTLSKey, "tls-key", "", "TLS private key file path (required for WebTransport)")
	flag.BoolVar(&flagTLSAuto, "tls-auto", false, "auto-generate self-signed TLS certificate for development")

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

	cred := sdk.NewCredential()

	serv := portal.NewRelayServer(cred, flagBootstraps)
	if flagMaxLease > 0 {
		serv.SetMaxRelayedPerLease(flagMaxLease)
	}

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

	// Register relay callback for BPS handling and IP tracking
	serv.SetEstablishRelayCallback(func(clientStream, leaseStream portal.Stream, leaseID string) {
		// Associate pending IP with this lease
		ipManager := admin.GetIPManager()
		if ipManager != nil {
			if ip := ipManager.PopPendingIP(); ip != "" {
				ipManager.RegisterLeaseIP(leaseID, ip)
			}
		}
		bpsManager := admin.GetBPSManager()
		manager.EstablishRelayWithBPS(clientStream, leaseStream, leaseID, bpsManager)
	})

	serv.Start()
	defer serv.Stop()

	// Setup TLS for WebTransport (HTTP/3)
	var tlsCert *tls.Certificate
	var certHash []byte

	if flagTLSAuto {
		cert, hash, err := generateSelfSignedCert()
		if err != nil {
			return fmt.Errorf("generate self-signed cert: %w", err)
		}
		tlsCert = &cert
		certHash = hash
		log.Info().
			Str("hash", fmt.Sprintf("%x", certHash)).
			Msg("[server] auto-generated TLS certificate (valid <14 days)")
	} else if flagTLSCert != "" && flagTLSKey != "" {
		cert, err := tls.LoadX509KeyPair(flagTLSCert, flagTLSKey)
		if err != nil {
			return fmt.Errorf("load TLS certificate: %w", err)
		}
		tlsCert = &cert
		log.Info().Msg("[server] loaded TLS certificate from files")
	}

	httpSrv := serveHTTP(fmt.Sprintf(":%d", flagPort), serv, admin, frontend, flagNoIndex, certHash, stop)

	var wtCleanup func()
	if tlsCert != nil {
		wtCleanup = serveWebTransport(fmt.Sprintf(":%d", flagPort), serv, tlsCert, stop)
	}

	<-ctx.Done()
	log.Info().Msg("[server] shutting down...")

	if wtCleanup != nil {
		wtCleanup()
	}

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
