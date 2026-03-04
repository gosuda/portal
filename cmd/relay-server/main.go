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

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/policy"
	"gosuda.org/portal/portal/sni"
	"gosuda.org/portal/types"
)

const (
	defaultAPIPort    = 4017
	defaultSNIPort    = 443
	defaultPortalURL  = "https://localhost:4017"
	defaultKeylessDir = "/etc/portal/keyless"
)

type relayServerConfig struct {
	AdminSecretKey    string
	PortalURL         string
	TrustedProxyCIDRs string
	KeylessDir        string
	CloudflareToken   string
	Bootstraps        []string
	AdminPort         int
	LeaseBPS          int
	SNIPort           int
	TrustProxyHeaders bool
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	cfg := relayServerConfig{}

	portalURL := strings.TrimSuffix(trimmedEnv("PORTAL_URL"), "/")
	if portalURL == "" {
		portalURL = defaultPortalURL
	}
	bootstrapsCSV := trimmedEnv("BOOTSTRAP_URIS")
	if bootstrapsCSV == "" {
		bootstrapsCSV = types.DefaultBootstrapFrom(portalURL)
	}
	sniPort := types.ParsePortNumber(os.Getenv("SNI_PORT"), defaultSNIPort)
	keylessDir := trimmedEnv("KEYLESS_DIR")
	if keylessDir == "" {
		keylessDir = defaultKeylessDir
	}
	adminSecretKey := trimmedEnv("ADMIN_SECRET_KEY")
	cloudflareToken := trimmedEnv("CLOUDFLARE_TOKEN")
	trustProxyHeaders := parseBoolEnv("TRUST_PROXY_HEADERS")
	trustedProxyCIDRs := trimmedEnv("TRUSTED_PROXY_CIDRS")

	flag.IntVar(&cfg.AdminPort, "adminport", defaultAPIPort, "Admin/HTTP server port")
	flag.StringVar(&cfg.AdminSecretKey, "admin-secret-key", adminSecretKey, "admin auth secret (env: ADMIN_SECRET_KEY)")
	flag.IntVar(&cfg.LeaseBPS, "lease-bps", 0, "bytes-per-second limit per lease (0=unlimited)")
	flag.StringVar(&cfg.PortalURL, "portal-url", portalURL, "portal base URL (env: PORTAL_URL)")
	flag.BoolVar(&cfg.TrustProxyHeaders, "trust-proxy-headers", trustProxyHeaders, "trust X-Forwarded-* and X-Real-IP headers (env: TRUST_PROXY_HEADERS)")
	flag.StringVar(&cfg.TrustedProxyCIDRs, "trusted-proxy-cidrs", trustedProxyCIDRs, "trusted proxy CIDR allowlist for forwarded headers, comma-separated (env: TRUSTED_PROXY_CIDRS)")
	flag.StringVar(&bootstrapsCSV, "bootstraps", bootstrapsCSV, "bootstrap URIs, comma-separated (env: BOOTSTRAP_URIS)")
	flag.IntVar(&cfg.SNIPort, "sni-port", sniPort, "SNI router port number (env: SNI_PORT)")
	flag.StringVar(&cfg.KeylessDir, "keyless-dir", keylessDir, "directory path for relay keyless materials (env: KEYLESS_DIR)")
	flag.StringVar(&cfg.CloudflareToken, "cloudflare-token", cloudflareToken, "Cloudflare DNS API token (Zone:Read + DNS:Edit) (env: CLOUDFLARE_TOKEN)")
	flag.Parse()

	cfg.Bootstraps = types.ParseURLs(bootstrapsCSV)
	parsedTrustedProxyCIDRs, err := parseTrustedProxyCIDRs(cfg.TrustedProxyCIDRs)
	if err != nil {
		log.Fatal().Err(err).Msg("parse trusted proxy CIDRs")
	}
	policy.SetTrustedProxyCIDRs(parsedTrustedProxyCIDRs)
	if err := runServer(cfg); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer(cfg relayServerConfig) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sniListenAddr := fmt.Sprintf(":%d", cfg.SNIPort)

	log.Info().
		Str("portal_base_url", cfg.PortalURL).
		Bool("trust_proxy_headers", cfg.TrustProxyHeaders).
		Str("trusted_proxy_cidrs", cfg.TrustedProxyCIDRs).
		Strs("bootstrap_uris", cfg.Bootstraps).
		Msg("[server] frontend configuration")

	rootHost := types.PortalRootHost(cfg.PortalURL)
	apiUpstreamAddr := types.LoopbackForwardAddr(fmt.Sprintf(":%d", cfg.AdminPort))
	serv, err := portal.NewRelayServer(ctx, cfg.Bootstraps, sniListenAddr, rootHost, cfg.KeylessDir, cfg.CloudflareToken)
	if err != nil {
		return fmt.Errorf("create relay server: %w", err)
	}

	frontend := NewFrontend(cfg.PortalURL)
	authManager := policy.NewAuthenticator(cfg.AdminSecretKey)
	admin := NewAdmin(int64(cfg.LeaseBPS), frontend, authManager, cfg.PortalURL, cfg.TrustProxyHeaders)
	frontend.SetAdmin(admin)

	// Load persisted admin settings (ban list, BPS limits, IP bans)
	admin.LoadSettings(serv)
	ipMgr := admin.GetIPManager()
	serv.GetLeaseManager().SetOnLeaseDeleted(func(leaseID string) {
		leaseID = strings.TrimSpace(leaseID)
		if leaseID == "" {
			return
		}

		serv.GetReverseHub().DropLease(leaseID)
		if sniRouter := serv.GetSNIRouter(); sniRouter != nil {
			sniRouter.UnregisterRouteByLeaseID(leaseID)
		}
		if ipMgr != nil {
			ipMgr.RemoveLeaseIP(leaseID)
		}
	})
	if ipMgr != nil {
		serv.GetReverseHub().SetIPBanChecker(func(ip string) bool {
			return policy.IsIPBannedByPolicy(ipMgr, ip)
		})
		serv.GetReverseHub().SetOnAccepted(func(leaseID, ip string) {
			if strings.TrimSpace(leaseID) == "" || strings.TrimSpace(ip) == "" {
				return
			}
			ipMgr.RegisterLeaseIP(leaseID, ip)
		})
	}

	// Set up SNI connection callback to route to tunnel backends
	serv.GetSNIRouter().SetConnectionCallback(func(clientConn net.Conn, route *sni.Route) {
		leaseID := ""
		if route != nil {
			leaseID = strings.TrimSpace(route.LeaseID)
		}
		if leaseID == "" {
			logSNIRouteWarning(route, nil, "[SNI] Missing lease id in route; dropping connection")
			closeSNIClientConn(clientConn, route)
			return
		}

		if _, ok := serv.GetLeaseManager().GetLeaseByID(leaseID); !ok {
			logSNIRouteWarning(route, nil, "[SNI] Lease not active; dropping connection and unregistering route")
			serv.GetSNIRouter().UnregisterRouteByLeaseID(leaseID)
			closeSNIClientConn(clientConn, route)
			return
		}

		// Get BPS manager for rate limiting
		bpsManager := admin.GetBPSManager()

		reverseConn, err := serv.GetReverseHub().AcquireForTLS(leaseID, portal.TLSAcquireWait)
		if err != nil {
			logSNIRouteWarning(route, err, "[SNI] Reverse tunnel unavailable")
			closeSNIClientConn(clientConn, route)
			return
		}

		// SNI path is reverse-only (NAT-friendly): relay never dials app directly.
		policy.EstablishRelayWithBPS(clientConn, reverseConn.Conn, leaseID, bpsManager)
		reverseConn.Close()
	})

	serv.ConfigurePortalRootFallback(rootHost, apiUpstreamAddr)

	if err := serv.Start(); err != nil {
		return fmt.Errorf("start relay server: %w", err)
	}
	defer serv.Stop()

	apiServ := serveAPI(fmt.Sprintf(":%d", cfg.AdminPort), serv, admin, frontend, cfg, stop)

	<-ctx.Done()
	log.Info().Msg("[server] shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if apiServ != nil {
		if err := apiServ.Shutdown(shutdownCtx); err != nil {
			log.Error().Err(err).Msg("[server] http server shutdown error")
		}
	}

	log.Info().Msg("[server] shutdown complete")
	return nil
}

func closeSNIClientConn(clientConn net.Conn, route *sni.Route) {
	if err := clientConn.Close(); err != nil {
		withSNIRouteFields(log.Debug().Err(err), route).Msg("[SNI] failed to close client connection")
	}
}

func logSNIRouteWarning(route *sni.Route, err error, msg string) {
	event := log.Warn()
	if err != nil {
		event = event.Err(err)
	}
	withSNIRouteFields(event, route).Msg(msg)
}

func withSNIRouteFields(event *zerolog.Event, route *sni.Route) *zerolog.Event {
	if route == nil {
		return event
	}
	return event.Str("lease_id", route.LeaseID).Str("sni", route.SNI)
}

func trimmedEnv(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

func parseBoolEnv(name string) bool {
	raw := trimmedEnv(name)
	return strings.EqualFold(raw, "true") || raw == "1"
}

func parseTrustedProxyCIDRs(raw string) ([]*net.IPNet, error) {
	return policy.ParseTrustedProxyCIDRs(raw)
}
