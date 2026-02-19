package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
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

type serverConfig struct {
	PortalURL      string
	PortalAppURL   string
	Bootstraps     []string
	ALPN           string
	Port           int
	MaxLease       int
	LeaseBPS       int
	NoIndex        bool
	AdminSecretKey string
	TLSCert        string
	TLSKey         string
	TLSAuto        bool
}

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

	var cfg serverConfig
	var flagBootstrapsCSV string
	flag.StringVar(&cfg.PortalURL, "portal-url", defaultPortalURL, "base URL for portal frontend (env: PORTAL_URL)")
	flag.StringVar(&cfg.PortalAppURL, "portal-app-url", defaultAppURL, "subdomain wildcard URL (env: PORTAL_APP_URL)")
	flag.StringVar(&flagBootstrapsCSV, "bootstraps", defaultBootstraps, "bootstrap addresses (comma-separated)")
	flag.StringVar(&cfg.ALPN, "alpn", "http/1.1", "ALPN identifier for this service")
	flag.IntVar(&cfg.Port, "port", 4017, "app UI and HTTP proxy port")
	flag.IntVar(&cfg.MaxLease, "max-lease", 0, "maximum active relayed connections per lease (0 = unlimited)")
	flag.IntVar(&cfg.LeaseBPS, "lease-bps", 0, "default bytes-per-second limit per lease (0 = unlimited)")

	defaultNoIndex := os.Getenv("NOINDEX") == "true"
	flag.BoolVar(&cfg.NoIndex, "noindex", defaultNoIndex, "disallow all crawlers via robots.txt (env: NOINDEX)")

	defaultAdminSecretKey := os.Getenv("ADMIN_SECRET_KEY")
	flag.StringVar(&cfg.AdminSecretKey, "admin-secret-key", defaultAdminSecretKey, "secret key for admin authentication (env: ADMIN_SECRET_KEY)")

	flag.StringVar(&cfg.TLSCert, "tls-cert", "", "TLS certificate file path (required for WebTransport)")
	flag.StringVar(&cfg.TLSKey, "tls-key", "", "TLS private key file path (required for WebTransport)")
	flag.BoolVar(&cfg.TLSAuto, "tls-auto", false, "auto-generate self-signed TLS certificate for development")

	flag.Parse()

	cfg.Bootstraps = utils.ParseURLs(flagBootstrapsCSV)
	if err := runServer(cfg); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer(cfg serverConfig) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info().
		Str("portal_base_url", cfg.PortalURL).
		Str("app_url", cfg.PortalAppURL).
		Str("bootstrap_uris", strings.Join(cfg.Bootstraps, ",")).
		Msg("[server] frontend configuration")

	cred := sdk.NewCredential()

	serv := portal.NewRelayServer(cred, cfg.Bootstraps)
	if cfg.MaxLease > 0 {
		serv.SetMaxRelayedPerLease(cfg.MaxLease)
	}

	// Create AuthManager for admin authentication
	// Auto-generate secret key if not provided
	adminSecretKey := cfg.AdminSecretKey
	if adminSecretKey == "" {
		randomBytes := make([]byte, 16)
		if _, err := rand.Read(randomBytes); err != nil {
			return fmt.Errorf("generate random admin secret key: %w", err)
		}
		adminSecretKey = hex.EncodeToString(randomBytes)
		log.Warn().
			Msg("[server] auto-generated ADMIN_SECRET_KEY (set ADMIN_SECRET_KEY env to use your own)")
		_, _ = fmt.Fprintf(os.Stderr, "AUTO_GENERATED_ADMIN_SECRET_KEY=%s\n", adminSecretKey)
	} else {
		log.Info().Msg("[server] admin authentication enabled")
	}
	authManager := manager.NewAuthManager(adminSecretKey)

	// Create Frontend first, then Admin, then attach Admin back to Frontend.
	frontend := NewFrontend(cfg.PortalURL, cfg.PortalAppURL)
	admin := NewAdmin(int64(cfg.LeaseBPS), frontend, authManager, cfg.PortalURL, cfg.PortalAppURL)
	frontend.SetAdmin(admin)

	// Load persisted admin settings (ban list, BPS limits, IP bans)
	admin.LoadSettings(serv)

	// Register relay callback for BPS handling and IP tracking
	serv.SetEstablishRelayCallback(func(clientStream, leaseStream portal.Stream, leaseID string) {
		ipManager := admin.GetIPManager()
		if ipManager != nil {
			if clientIP := streamClientIP(leaseStream); clientIP != "" {
				ipManager.RegisterLeaseIP(leaseID, clientIP)
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

	if cfg.TLSAuto {
		cert, hash, err := utils.GenerateSelfSignedCert()
		if err != nil {
			return fmt.Errorf("generate self-signed cert: %w", err)
		}
		tlsCert = &cert
		certHash = hash
		log.Info().
			Str("hash", hex.EncodeToString(certHash)).
			Msg("[server] auto-generated TLS certificate (valid <14 days)")
		// Force HTTPS scheme for auto-generated TLS
		cfg.PortalURL = strings.Replace(cfg.PortalURL, "http://", "https://", 1)
		cfg.PortalAppURL = strings.Replace(cfg.PortalAppURL, "http://", "https://", 1)
	} else if cfg.TLSCert != "" && cfg.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return fmt.Errorf("load TLS certificate: %w", err)
		}
		tlsCert = &cert
		log.Info().Msg("[server] loaded TLS certificate from files")
	}

	if (cfg.TLSCert != "") != (cfg.TLSKey != "") {
		return errors.New("both --tls-cert and --tls-key must be provided together")
	}

	httpSrv := serveHTTP(fmt.Sprintf(":%d", cfg.Port), serv, admin, frontend, cfg.NoIndex, certHash, cfg.PortalAppURL, cfg.PortalURL, stop)

	var wtCleanup func()
	if tlsCert != nil {
		wtCleanup = serveWebTransport(fmt.Sprintf(":%d", cfg.Port), serv, admin, tlsCert, stop)
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
