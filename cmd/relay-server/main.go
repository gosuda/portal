package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal"
	"github.com/gosuda/portal/v2/portal/acme"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

const (
	defaultAPIPort      = 4017
	defaultSNIPort      = 443
	defaultUDPPortCount = 0
	defaultPortalURL    = "https://localhost:4017"
	defaultKeylessDir   = "./.portal-certs"
)

type relayServerConfig struct {
	PortalURL          string
	OwnerPrivateKey    string
	Bootstraps         string
	APIPort            int
	SNIPort            int
	UDPPortCount       int
	AdminSecretKey     string
	DiscoveryEnabled   bool
	TrustProxyHeaders  bool
	TrustedProxyCIDRs  string
	KeylessDir         string
	ACMEDNSProvider    string
	CloudflareToken    string
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSSessionToken    string
	AWSRegion          string
	AWSHostedZoneID    string
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	logger := log.With().Str("component", "relay-server").Logger()

	cfg := relayServerConfig{}

	portalURL := strings.TrimSuffix(trimmedEnv("PORTAL_URL"), "/")
	if portalURL == "" {
		portalURL = defaultPortalURL
	}
	apiPort := parsePortNumber(os.Getenv("API_PORT"), defaultAPIPort)
	sniPort := parsePortNumber(os.Getenv("SNI_PORT"), defaultSNIPort)
	udpPortCount := parseNonNegativeInt(os.Getenv("UDP_PORT_COUNT"), defaultUDPPortCount)
	ownerPrivateKey := trimmedEnv("OWNER_PRIVATE_KEY")
	bootstraps := trimmedEnv("BOOTSTRAPS")
	adminSecretKey := trimmedEnv("ADMIN_SECRET_KEY")
	discoveryEnabled := utils.ParseBoolEnv("DISCOVERY_ENABLED", false)
	trustProxyHeaders := utils.ParseBoolEnv("TRUST_PROXY_HEADERS", false)
	trustedProxyCIDRs := trimmedEnv("TRUSTED_PROXY_CIDRS")
	keylessDir := trimmedEnv("KEYLESS_DIR")
	if keylessDir == "" {
		keylessDir = defaultKeylessDir
	}
	adminSettingsPath = filepath.Join(keylessDir, "admin_settings.json")
	acmeDNSProvider := trimmedEnv("ACME_DNS_PROVIDER")
	if acmeDNSProvider == "" {
		acmeDNSProvider = "cloudflare"
	}
	cloudflareToken := trimmedEnv("CLOUDFLARE_TOKEN")
	awsAccessKeyID := trimmedEnv("AWS_ACCESS_KEY_ID")
	awsSecretAccessKey := trimmedEnv("AWS_SECRET_ACCESS_KEY")
	awsSessionToken := trimmedEnv("AWS_SESSION_TOKEN")
	awsRegion := trimmedEnv("AWS_REGION")
	if awsRegion == "" {
		awsRegion = trimmedEnv("AWS_DEFAULT_REGION")
	}
	awsHostedZoneID := trimmedEnv("AWS_HOSTED_ZONE_ID")

	flag.StringVar(&cfg.PortalURL, "portal-url", portalURL, "portal base URL (env: PORTAL_URL)")
	flag.IntVar(&cfg.APIPort, "api-port", apiPort, "Admin/API server port (env: API_PORT)")
	flag.IntVar(&cfg.SNIPort, "sni-port", sniPort, "TCP SNI router port number (env: SNI_PORT)")
	flag.IntVar(&cfg.UDPPortCount, "udp-port-count", udpPortCount, "Number of UDP ports to allocate for leases, starting at port 50000 (0=disabled) (env: UDP_PORT_COUNT)")

	flag.StringVar(&cfg.OwnerPrivateKey, "owner-private-key", ownerPrivateKey, "relay owner private key used to derive a discovery address (env: OWNER_PRIVATE_KEY)")
	flag.StringVar(&cfg.Bootstraps, "bootstraps", bootstraps, "additional bootstrap relay API URLs used for discovery expansion (env: BOOTSTRAPS)")
	flag.StringVar(&cfg.AdminSecretKey, "admin-secret-key", adminSecretKey, "admin auth secret (env: ADMIN_SECRET_KEY)")
	flag.BoolVar(&cfg.DiscoveryEnabled, "discovery", discoveryEnabled, "serve relay discovery endpoints and poll discovery peers (env: DISCOVERY_ENABLED)")
	flag.BoolVar(&cfg.TrustProxyHeaders, "trust-proxy-headers", trustProxyHeaders, "trust X-Forwarded-* and X-Real-IP headers from trusted proxies (env: TRUST_PROXY_HEADERS)")
	flag.StringVar(&cfg.TrustedProxyCIDRs, "trusted-proxy-cidrs", trustedProxyCIDRs, "trusted proxy CIDR allowlist for forwarded headers, comma-separated; defaults to private/loopback proxy ranges when trust-proxy-headers is enabled (env: TRUSTED_PROXY_CIDRS)")

	flag.StringVar(&cfg.KeylessDir, "keyless-dir", keylessDir, "directory path for relay keyless materials (env: KEYLESS_DIR)")
	flag.StringVar(&cfg.ACMEDNSProvider, "acme-dns-provider", acmeDNSProvider, "ACME DNS provider for DNS-01 and A-record sync (cloudflare|route53) (env: ACME_DNS_PROVIDER)")
	flag.StringVar(&cfg.CloudflareToken, "cloudflare-token", cloudflareToken, "Cloudflare DNS API token (required when acme-dns-provider=cloudflare) (env: CLOUDFLARE_TOKEN)")
	flag.StringVar(&cfg.AWSAccessKeyID, "aws-access-key-id", awsAccessKeyID, "AWS access key ID for Route53 static credentials; uses the default AWS credential chain when omitted (env: AWS_ACCESS_KEY_ID)")
	flag.StringVar(&cfg.AWSSecretAccessKey, "aws-secret-access-key", awsSecretAccessKey, "AWS secret access key for Route53 static credentials (env: AWS_SECRET_ACCESS_KEY)")
	flag.StringVar(&cfg.AWSSessionToken, "aws-session-token", awsSessionToken, "AWS session token for Route53 temporary credentials (env: AWS_SESSION_TOKEN)")
	flag.StringVar(&cfg.AWSRegion, "aws-region", awsRegion, "AWS region for Route53 and Route53-backed DNS-01; defaults to us-east-1 when unset (env: AWS_REGION or AWS_DEFAULT_REGION)")
	flag.StringVar(&cfg.AWSHostedZoneID, "aws-hosted-zone-id", awsHostedZoneID, "explicit Route53 hosted zone ID override (env: AWS_HOSTED_ZONE_ID)")
	flag.Parse()

	logger.Info().
		Str("release_version", types.ReleaseVersion).
		Str("portal_url", cfg.PortalURL).
		Bool("discovery_enabled", cfg.DiscoveryEnabled).
		Bool("udp_enabled", cfg.UDPPortCount > 0).
		Msg("configured relay server")

	if err := runServer(cfg); err != nil {
		logger.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer(cfg relayServerConfig) error {
	logger := log.With().Str("component", "relay-server").Logger()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rootHost := utils.PortalRootHost(cfg.PortalURL)
	apiListenAddr := fmt.Sprintf(":%d", cfg.APIPort)
	sniListenAddr := fmt.Sprintf(":%d", cfg.SNIPort)
	trustedProxyCIDRs, err := utils.ParseCIDRs(cfg.TrustedProxyCIDRs)
	if err != nil {
		return fmt.Errorf("parse trusted proxy cidrs: %w", err)
	}
	bootstraps, err := utils.NormalizeRelayURLs(utils.SplitCSV(cfg.Bootstraps))
	if err != nil {
		return fmt.Errorf("normalize bootstraps: %w", err)
	}
	if strings.TrimSpace(cfg.OwnerPrivateKey) == "" {
		cfg.OwnerPrivateKey, err = loadOwnerPrivateKey(cfg.KeylessDir)
		if err != nil {
			return fmt.Errorf("load relay owner private key: %w", err)
		}
	}
	previousOwnerPrivateKey := cfg.OwnerPrivateKey
	server, err := portal.NewServer(portal.ServerConfig{
		PortalURL:       cfg.PortalURL,
		OwnerPrivateKey: cfg.OwnerPrivateKey,
		Bootstraps:      bootstraps,
		ACME: acme.Config{
			KeyDir:             cfg.KeylessDir,
			DNSProvider:        cfg.ACMEDNSProvider,
			CloudflareToken:    cfg.CloudflareToken,
			AWSAccessKeyID:     cfg.AWSAccessKeyID,
			AWSSecretAccessKey: cfg.AWSSecretAccessKey,
			AWSSessionToken:    cfg.AWSSessionToken,
			AWSRegion:          cfg.AWSRegion,
			AWSHostedZoneID:    cfg.AWSHostedZoneID,
		},
		APIListenAddr:     apiListenAddr,
		SNIListenAddr:     sniListenAddr,
		TrustedProxyCIDRs: trustedProxyCIDRs,
		TrustProxyHeaders: cfg.TrustProxyHeaders,
		DiscoveryEnabled:  cfg.DiscoveryEnabled,
		UDPPortCount:      cfg.UDPPortCount,
	})
	if err != nil {
		return fmt.Errorf("create relay server: %w", err)
	}
	if identity := server.OwnerIdentity(); identity.PrivateKey != "" {
		cfg.OwnerPrivateKey = identity.PrivateKey
	}
	if cfg.OwnerPrivateKey != previousOwnerPrivateKey {
		if err := saveOwnerPrivateKey(cfg.KeylessDir, cfg.OwnerPrivateKey); err != nil {
			return fmt.Errorf("persist relay owner private key: %w", err)
		}
	}

	frontend, err := NewFrontend(cfg.PortalURL, server, cfg.AdminSecretKey, trustedProxyCIDRs, cfg.TrustProxyHeaders)
	if err != nil {
		return fmt.Errorf("create frontend: %w", err)
	}

	if err := server.Start(ctx, frontend.Handler()); err != nil {
		return fmt.Errorf("start relay server: %w", err)
	}

	logEvent := logger.Info().
		Str("api_addr", utils.HostPortOrLoopback(server.APIAddr())).
		Str("sni_addr", server.SNIAddr()).
		Str("root_host", rootHost).
		Str("acme_dns_provider", cfg.ACMEDNSProvider).
		Bool("discovery_enabled", server.DiscoveryEnabled()).
		Bool("udp_enabled", cfg.UDPPortCount > 0).
		Bool("acme_enabled", !strings.HasSuffix(rootHost, "localhost") && rootHost != "127.0.0.1" && rootHost != "::1")
	if quicAddr := server.QUICTunnelAddr(); quicAddr != "" {
		logEvent = logEvent.Str("internal_quic_tunnel_addr", quicAddr)
	}
	logEvent.Msg("relay server started")

	return server.Wait()
}

func trimmedEnv(name string) string {
	return strings.TrimSpace(os.Getenv(name))
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

func parseNonNegativeInt(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	var v int
	if _, err := fmt.Sscanf(raw, "%d", &v); err != nil || v < 0 {
		return fallback
	}
	return v
}

func ownerPrivateKeyPath(keylessDir string) string {
	return filepath.Join(strings.TrimSpace(keylessDir), "owner_private_key.hex")
}

func loadOwnerPrivateKey(keylessDir string) (string, error) {
	keyPath := ownerPrivateKeyPath(keylessDir)
	data, err := os.ReadFile(keyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func saveOwnerPrivateKey(keylessDir, privateKey string) error {
	privateKey = strings.TrimSpace(privateKey)
	if privateKey == "" {
		return nil
	}
	keyPath := ownerPrivateKeyPath(keylessDir)
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(keyPath, []byte(privateKey+"\n"), 0o600)
}
