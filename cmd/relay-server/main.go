package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
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
	flagPortalURL        string
	flagPortalAppURL     string
	flagPort             int
	flagNoIndex          bool
	flagLeaseBPS         int
	flagMaxConnsPerLease int
	flagAdminSecretKey   string

	// Funnel (TLS SNI passthrough) flags
	flagFunnelPort   int
	flagFunnelDomain string

	// ACME automatic certificate provisioning flags
	flagACMECacheDir string
	flagACMEPort     int
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
	flag.IntVar(&flagMaxConnsPerLease, "max-connections-per-lease", 0, "default max concurrent connections per lease (0 = unlimited)")

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
	defaultFunnelDomain := os.Getenv("FUNNEL_DOMAIN")
	if defaultFunnelDomain == "" {
		defaultFunnelDomain = "localhost"
	}
	flag.StringVar(&flagFunnelDomain, "funnel-domain", defaultFunnelDomain, "base domain for funnel subdomains, e.g. portal.example.com (env: FUNNEL_DOMAIN)")

	// ACME flags
	defaultACMECacheDir := os.Getenv("ACME_CACHE_DIR")
	if defaultACMECacheDir == "" {
		defaultACMECacheDir = "acme-cache"
	}
	flag.StringVar(&flagACMECacheDir, "acme-cache-dir", defaultACMECacheDir, "directory for ACME certificate cache (env: ACME_CACHE_DIR)")
	defaultACMEPort := 80
	if envACMEPort := os.Getenv("ACME_PORT"); envACMEPort != "" {
		if v, err := strconv.Atoi(envACMEPort); err == nil {
			defaultACMEPort = v
		}
	}
	flag.IntVar(&flagACMEPort, "acme-port", defaultACMEPort, "HTTP port for ACME HTTP-01 challenges (env: ACME_PORT)")

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
	admin := NewAdmin(int64(flagLeaseBPS), int64(flagMaxConnsPerLease), frontend, authManager)
	frontend.SetAdmin(admin)

	// Load persisted admin settings (ban list, BPS limits, IP bans)
	admin.LoadSettings(lm)

	// Initialize funnel components.
	// ACME is automatic when funnel-domain is a real domain (not "localhost").
	// For localhost/dev, a self-signed wildcard certificate is used as fallback.
	var funnelRegistry *Registry
	acmeEnabled := flagFunnelDomain != "localhost"

	// Generate self-signed fallback cert for localhost/dev mode.
	var fallbackCert, fallbackKey []byte
	if !acmeEnabled {
		var err error
		fallbackCert, fallbackKey, err = generateSelfSignedCert(flagFunnelDomain)
		if err != nil {
			log.Fatal().Err(err).Msg("[funnel] failed to generate self-signed certificate")
		}
		log.Warn().Str("domain", "*."+flagFunnelDomain).Msg("[funnel] using self-signed certificate (set --funnel-domain for automatic ACME)")
	}

	certMgr := NewCertManager(flagFunnelDomain, flagACMECacheDir, acmeEnabled, fallbackCert, fallbackKey)

	{

		// Start HTTP-01 challenge server for ACME when using a real domain.
		if acmeEnabled {
			go func() {
				acmeSrv := &http.Server{
					Addr:    fmt.Sprintf(":%d", flagACMEPort),
					Handler: certMgr.HTTPHandler(http.HandlerFunc(redirectHTTPS)),
				}
				log.Info().Int("port", flagACMEPort).Str("domain", flagFunnelDomain).Msg("[acme] HTTP-01 challenge server started")
				if err := acmeSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Error().Err(err).Msg("[acme] HTTP-01 server error")
				}
			}()
		}

		hub := portal.NewReverseHub()
		defer hub.Stop()

		router := sni.NewRouter(fmt.Sprintf(":%d", flagFunnelPort))

		funnelRegistry = NewRegistry(lm, router, hub, admin.GetIPManager(), certMgr, flagFunnelDomain)

		// Wire ReverseHub authorizer: validate token against lease
		hub.SetAuthorizer(func(leaseID, token string) bool {
			entry, ok := lm.GetLeaseByID(leaseID)
			if !ok {
				return false
			}
			return len(entry.ReverseToken) > 0 &&
				subtle.ConstantTimeCompare([]byte(entry.ReverseToken), []byte(token)) == 1
		})

		// Wire cleanup callback: when a lease is deleted, clean up SNI route + ReverseHub + BPS + conn limits
		lm.SetOnLeaseDeleted(func(leaseID string) {
			router.UnregisterRouteByLeaseID(leaseID)
			hub.DropLease(leaseID)
			if bpsMgr := admin.GetBPSManager(); bpsMgr != nil {
				bpsMgr.CleanupLease(leaseID)
			}
			if connLimitMgr := admin.GetConnLimitManager(); connLimitMgr != nil {
				connLimitMgr.CleanupLease(leaseID)
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

			// Enforce per-lease concurrent connection limit
			if connLimitMgr := admin.GetConnLimitManager(); connLimitMgr != nil {
				if !connLimitMgr.TryAcquire(route.LeaseID) {
					log.Debug().Str("lease_id", route.LeaseID).Str("sni", route.SNI).Msg("[funnel] connection rejected: per-lease limit reached")
					_ = conn.Close()
					return
				}
				defer connLimitMgr.Release(route.LeaseID)
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

	// Start HTTPS admin server when ACME is enabled, so the base domain
	// (e.g. https://portal.example.com) serves the admin UI with a valid cert.
	var httpsSrv *http.Server
	if acmeEnabled {
		if tlsConfig := certMgr.TLSConfig(); tlsConfig != nil {
			httpsSrv = serveHTTPS(":4018", tlsConfig, lm, admin, frontend, flagNoIndex, funnelRegistry, stop)
		}
	}

	<-ctx.Done()
	log.Info().Msg("[server] shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if httpsSrv != nil {
		if err := httpsSrv.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("[server] https server shutdown error")
		}
	}
	if httpSrv != nil {
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("[server] http server shutdown error")
		}
	}

	log.Info().Msg("[server] shutdown complete")
	return nil
}

// redirectHTTPS redirects HTTP requests to HTTPS. Used as fallback handler
// for the ACME HTTP-01 challenge server on port 80.
func redirectHTTPS(w http.ResponseWriter, r *http.Request) {
	target := "https://" + r.Host + r.URL.RequestURI()
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}
