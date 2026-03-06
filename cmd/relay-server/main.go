package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	defaultAPIPort    = 4017
	defaultSNIPort    = 443
	defaultPortalURL  = "https://localhost:4017"
	defaultKeylessDir = ".portal-certs"
)

type relayServerConfig struct {
	PortalURL         string
	Bootstraps        []string
	APIPort           int
	SNIPort           int
	AdminSecretKey    string
	TrustProxyHeaders bool
	TrustedProxyCIDRs string
	KeylessDir        string
	CloudflareToken   string
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	logger := log.With().Str("component", "relay-server").Logger()

	cfg := relayServerConfig{}

	portalURL := strings.TrimSuffix(trimmedEnv("PORTAL_URL"), "/")
	if portalURL == "" {
		portalURL = defaultPortalURL
	}
	bootstrapsCSV := trimmedEnv("BOOTSTRAP_URIS")
	if bootstrapsCSV == "" {
		bootstrapsCSV = portalURL
	}
	apiPort := parsePortNumber(os.Getenv("API_PORT"), defaultAPIPort)
	sniPort := parsePortNumber(os.Getenv("SNI_PORT"), defaultSNIPort)
	adminSecretKey := trimmedEnv("ADMIN_SECRET_KEY")
	trustProxyHeaders := parseBoolEnv("TRUST_PROXY_HEADERS")
	trustedProxyCIDRs := trimmedEnv("TRUSTED_PROXY_CIDRS")
	keylessDir := trimmedEnv("KEYLESS_DIR")
	if keylessDir == "" {
		keylessDir = defaultKeylessDir
	}
	cloudflareToken := trimmedEnv("CLOUDFLARE_TOKEN")

	flag.StringVar(&cfg.PortalURL, "portal-url", portalURL, "portal base URL (env: PORTAL_URL)")
	flag.StringVar(&bootstrapsCSV, "bootstraps", bootstrapsCSV, "bootstrap URIs, comma-separated (env: BOOTSTRAP_URIS)")
	flag.IntVar(&cfg.APIPort, "api-port", apiPort, "Admin/API server port (env: API_PORT)")
	flag.IntVar(&cfg.SNIPort, "sni-port", sniPort, "SNI router port number (env: SNI_PORT)")

	flag.StringVar(&cfg.AdminSecretKey, "admin-secret-key", adminSecretKey, "admin auth secret (env: ADMIN_SECRET_KEY)")
	flag.BoolVar(&cfg.TrustProxyHeaders, "trust-proxy-headers", trustProxyHeaders, "trust X-Forwarded-* and X-Real-IP headers from trusted proxies (env: TRUST_PROXY_HEADERS)")
	flag.StringVar(&cfg.TrustedProxyCIDRs, "trusted-proxy-cidrs", trustedProxyCIDRs, "trusted proxy CIDR allowlist for forwarded headers, comma-separated; defaults to private/loopback proxy ranges when trust-proxy-headers is enabled (env: TRUSTED_PROXY_CIDRS)")

	flag.StringVar(&cfg.KeylessDir, "keyless-dir", keylessDir, "directory path for relay keyless materials (env: KEYLESS_DIR)")
	flag.StringVar(&cfg.CloudflareToken, "cloudflare-token", cloudflareToken, "Cloudflare DNS API token (Zone:Read + DNS:Edit) (env: CLOUDFLARE_TOKEN)")
	flag.Parse()

	cfg.Bootstraps = parseURLs(bootstrapsCSV)
	if len(cfg.Bootstraps) == 0 {
		cfg.Bootstraps = []string{cfg.PortalURL}
	}
	if cfg.PortalURL == "" {
		cfg.PortalURL = cfg.Bootstraps[0]
	}

	logger.Info().
		Str("portal_url", cfg.PortalURL).
		Strs("bootstraps", cfg.Bootstraps).
		Msg("configured relay server")

	if err := runServer(cfg); err != nil {
		logger.Fatal().Err(err).Msg("execute root command")
	}
}

func trimmedEnv(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

func parseBoolEnv(name string) bool {
	raw := trimmedEnv(name)
	return strings.EqualFold(raw, "true") || raw == "1"
}

func parsePortNumber(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	var port int
	if _, err := fmt.Sscanf(raw, "%d", &port); err != nil || port < 1 || port > 65535 {
		return fallback
	}
	return port
}
