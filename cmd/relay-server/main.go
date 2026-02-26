package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
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
	flagPort           int
	flagNoIndex        bool
	flagLeaseBPS       int
	flagAdminSecretKey string

	// Funnel (TLS SNI passthrough) flags
	flagFunnelPort   int
	flagTLSCert      string
	flagTLSKey       string
	flagFunnelDomain string
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

	flag.StringVar(&flagPortalURL, "portal-url", defaultPortalURL, "base URL for portal frontend (env: PORTAL_URL)")
	flag.StringVar(&flagPortalAppURL, "portal-app-url", defaultAppURL, "subdomain wildcard URL (env: PORTAL_APP_URL)")
	flag.IntVar(&flagPort, "port", 4017, "app UI and HTTP proxy port")
	flag.IntVar(&flagLeaseBPS, "lease-bps", 0, "default bytes-per-second limit per lease (0 = unlimited)")

	defaultNoIndex := os.Getenv("NOINDEX") == "true"
	flag.BoolVar(&flagNoIndex, "noindex", defaultNoIndex, "disallow all crawlers via robots.txt (env: NOINDEX)")

	defaultAdminSecretKey := os.Getenv("ADMIN_SECRET_KEY")
	flag.StringVar(&flagAdminSecretKey, "admin-secret-key", defaultAdminSecretKey, "secret key for admin authentication (env: ADMIN_SECRET_KEY)")

	// Funnel flags
	defaultFunnelPort := 443
	if envFunnelPort := os.Getenv("FUNNEL_PORT"); envFunnelPort != "" {
		if v, err := strconv.Atoi(envFunnelPort); err == nil {
			defaultFunnelPort = v
		}
	}
	flag.IntVar(&flagFunnelPort, "funnel-port", defaultFunnelPort, "TCP port for SNI router (env: FUNNEL_PORT)")
	flag.StringVar(&flagTLSCert, "tls-cert", os.Getenv("TLS_CERT"), "path to wildcard TLS certificate PEM file (env: TLS_CERT)")
	flag.StringVar(&flagTLSKey, "tls-key", os.Getenv("TLS_KEY"), "path to wildcard TLS private key PEM file (env: TLS_KEY)")
	defaultFunnelDomain := os.Getenv("FUNNEL_DOMAIN")
	if defaultFunnelDomain == "" {
		defaultFunnelDomain = "localhost"
	}
	flag.StringVar(&flagFunnelDomain, "funnel-domain", defaultFunnelDomain, "base domain for funnel subdomains, e.g. portal.example.com (env: FUNNEL_DOMAIN)")
	flag.Parse()

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
		Msg("[server] frontend configuration")

	// Create LeaseManager directly (no more RelayServer wrapper).
	lm := portal.NewLeaseManager(30 * time.Second)
	lm.Start()
	defer lm.Stop()

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
		preview := flagAdminSecretKey
		if len(preview) > 4 {
			preview = preview[:4] + "****"
		} else {
			preview = "****"
		}
		log.Info().Str("key_prefix", preview).Msg("[server] admin authentication enabled with provided secret key")
	}
	authManager := manager.NewAuthManager(flagAdminSecretKey)

	// Create Frontend first, then Admin, then attach Admin back to Frontend.
	frontend := NewFrontend()
	admin := NewAdmin(int64(flagLeaseBPS), frontend, authManager)
	frontend.SetAdmin(admin)

	// Load persisted admin settings (ban list, BPS limits, IP bans)
	admin.LoadSettings(lm)

	// Initialize funnel components.
	// If TLS cert+key files are provided, use them. Otherwise auto-generate a
	// self-signed wildcard certificate for the funnel domain.
	var funnelRegistry *Registry
	{
		var certPEM, keyPEM []byte

		if flagTLSCert != "" && flagTLSKey != "" {
			var err error
			certPEM, err = os.ReadFile(flagTLSCert)
			if err != nil {
				log.Fatal().Err(err).Str("path", flagTLSCert).Msg("[funnel] failed to read TLS certificate")
			}
			keyPEM, err = os.ReadFile(flagTLSKey)
			if err != nil {
				log.Fatal().Err(err).Str("path", flagTLSKey).Msg("[funnel] failed to read TLS key")
			}
			if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
				log.Fatal().Err(err).Msg("[funnel] invalid TLS certificate/key pair")
			}
		} else {
			var err error
			certPEM, keyPEM, err = generateSelfSignedCert(flagFunnelDomain)
			if err != nil {
				log.Fatal().Err(err).Msg("[funnel] failed to generate self-signed certificate")
			}
			log.Warn().Str("domain", "*."+flagFunnelDomain).Msg("[funnel] using auto-generated self-signed certificate (provide --tls-cert/--tls-key for production)")
		}

		hub := portal.NewReverseHub()
		defer hub.Stop()

		router := sni.NewRouter(fmt.Sprintf(":%d", flagFunnelPort))

		funnelRegistry = NewRegistry(lm, router, hub, admin.GetIPManager(), certPEM, keyPEM, flagFunnelDomain)

		// Wire ReverseHub authorizer: validate token against lease
		hub.SetAuthorizer(func(leaseID, token string) bool {
			entry, ok := lm.GetLeaseByID(leaseID)
			if !ok {
				return false
			}
			return len(entry.ReverseToken) > 0 &&
				subtle.ConstantTimeCompare([]byte(entry.ReverseToken), []byte(token)) == 1
		})

		// Wire cleanup callback: when a lease is deleted, clean up SNI route + ReverseHub + BPS
		lm.SetOnLeaseDeleted(func(leaseID string) {
			router.UnregisterRouteByLeaseID(leaseID)
			hub.DropLease(leaseID)
			if bpsMgr := admin.GetBPSManager(); bpsMgr != nil {
				bpsMgr.CleanupLease(leaseID)
			}
		})

		// Wire SNI router -> ReverseHub: when a browser connects via TLS, acquire a reverse connection
		router.SetConnectionCallback(func(conn net.Conn, route *sni.Route) {
			// Enforce approval mode: reject connections to unapproved leases
			if approveManager := admin.GetApproveManager(); approveManager != nil {
				if approveManager.GetApprovalMode() == manager.ApprovalModeManual &&
					!approveManager.IsLeaseApproved(route.LeaseID) {
					log.Debug().Str("lease_id", route.LeaseID).Str("sni", route.SNI).Msg("[funnel] connection rejected: lease not approved")
					_ = conn.Close()
					return
				}
			}

			reverseConn, err := hub.AcquireStarted(route.LeaseID, 10*time.Second)
			if err != nil {
				log.Debug().Err(err).Str("lease_id", route.LeaseID).Str("sni", route.SNI).Msg("[funnel] no reverse connection available")
				_ = conn.Close()
				return
			}
			defer reverseConn.Close()

			if bpsManager := admin.GetBPSManager(); bpsManager != nil {
				if bucket := bpsManager.GetBucket(route.LeaseID); bucket != nil {
					sni.BridgeConnectionsWithCopy(conn, reverseConn.Conn, func(dst io.Writer, src io.Reader) (int64, error) {
						return manager.Copy(dst, src, bucket)
					})
					return
				}
			}
			sni.BridgeConnections(conn, reverseConn.Conn)
		})

		// Start SNI router
		go func() {
			if err := router.ListenAndServe(); err != nil {
				log.Error().Err(err).Msg("[funnel] SNI router error")
				stop()
			}
		}()

		log.Info().
			Int("port", flagFunnelPort).
			Str("domain", flagFunnelDomain).
			Msg("[funnel] TLS SNI passthrough enabled")
	}

	httpSrv := serveHTTP(fmt.Sprintf(":%d", flagPort), lm, admin, frontend, flagNoIndex, funnelRegistry, stop)

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
