package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal"
	"github.com/gosuda/portal/v2/portal/acme"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

func main() {
	log.Logger = log.Output(zerolog.NewConsoleWriter())
	if err := utils.RunCommands(os.Args[1:], os.Stdout, os.Stderr, printRootUsage, map[string]utils.CommandFunc{
		"":      runServeCommand,
		"serve": runServeCommand,
		"help":  runHelpCommand,
	}); err != nil {
		log.Error().Err(err).Msg("execute root command")
		os.Exit(1)
	}
}

type relayServerConfig struct {
	PortalURL           string
	APIPort             int
	SNIPort             int
	UDPPortCount        int
	LandingPageEnabled  bool
	Bootstraps          string
	DiscoveryEnabled    bool
	OwnerPrivateKey     string
	WireGuardPrivateKey string
	DiscoveryPort       int
	AdminSecretKey      string
	TrustProxyHeaders   bool
	TrustedProxyCIDRs   string
	AdminSettingsPath   string
	KeylessDir          string
	ACMEDNSProvider     string
	CloudflareToken     string
	AWSAccessKeyID      string
	AWSSecretAccessKey  string
	AWSSessionToken     string
	AWSRegion           string
	AWSHostedZoneID     string
}

func runServeCommand(args []string) error {
	cfg := relayServerConfig{}
	fs := utils.NewFlagSet("relay-server", printRootUsage)

	utils.StringFlagEnv(fs, &cfg.PortalURL, "portal-url", "https://localhost:4017", "portal base URL", "PORTAL_URL")
	utils.IntFlagEnv(fs, &cfg.APIPort, "api-port", 4017, utils.ParsePortNumber, "Admin/API server port", "API_PORT")
	utils.IntFlagEnv(fs, &cfg.SNIPort, "sni-port", 443, utils.ParsePortNumber, "TCP SNI router port number", "SNI_PORT")
	utils.IntFlagEnv(fs, &cfg.UDPPortCount, "udp-port-count", 0, utils.ParseNonNegativeInt, "Number of UDP ports to allocate for leases, starting at port 50000 (0=disabled)", "UDP_PORT_COUNT")
	utils.BoolFlagEnv(fs, &cfg.LandingPageEnabled, "landing-page-enabled", false, "enable landing page by default when no admin setting has been saved yet", "LANDING_PAGE_ENABLED")
	utils.StringFlagEnv(fs, &cfg.Bootstraps, "bootstraps", "", "additional bootstrap relay API URLs used for discovery expansion", "BOOTSTRAPS")
	utils.BoolFlagEnv(fs, &cfg.DiscoveryEnabled, "discovery", false, "serve relay discovery endpoints and poll discovery peers", "DISCOVERY")
	utils.StringFlagEnv(fs, &cfg.OwnerPrivateKey, "owner-private-key", "", "relay owner private key used to derive a discovery address", "OWNER_PRIVATE_KEY")
	utils.StringFlagEnv(fs, &cfg.WireGuardPrivateKey, "wireguard-private-key", "", "wireguard private key for relay peer overlay", "WIREGUARD_PRIVATE_KEY")
	utils.IntFlagEnv(fs, &cfg.DiscoveryPort, "discovery-port", 0, utils.ParsePortNumber, "public UDP listen port advertised for relay-peer discovery overlay (defaults to 51820 when wireguard is enabled)", "DISCOVERY_PORT")
	utils.StringFlagEnv(fs, &cfg.AdminSecretKey, "admin-secret-key", "", "admin auth secret", "ADMIN_SECRET_KEY")
	utils.BoolFlagEnv(fs, &cfg.TrustProxyHeaders, "trust-proxy-headers", false, "trust X-Forwarded-* and X-Real-IP headers from trusted proxies", "TRUST_PROXY_HEADERS")
	utils.StringFlagEnv(fs, &cfg.TrustedProxyCIDRs, "trusted-proxy-cidrs", "", "trusted proxy CIDR allowlist for forwarded headers, comma-separated; defaults to private/loopback proxy ranges when trust-proxy-headers is enabled", "TRUSTED_PROXY_CIDRS")

	utils.StringFlagEnv(fs, &cfg.KeylessDir, "keyless-dir", "./.portal-certs", "directory path for relay keyless materials", "KEYLESS_DIR")
	utils.StringFlagEnv(fs, &cfg.AdminSettingsPath, "admin-settings-path", "admin_settings.json", "admin settings file path", "ADMIN_SETTINGS_PATH")
	utils.StringFlagEnv(fs, &cfg.ACMEDNSProvider, "acme-dns-provider", "cloudflare", "ACME DNS provider for DNS-01 and A-record sync (cloudflare|route53)", "ACME_DNS_PROVIDER")
	utils.StringFlagEnv(fs, &cfg.CloudflareToken, "cloudflare-token", "", "Cloudflare DNS API token (required when acme-dns-provider=cloudflare)", "CLOUDFLARE_TOKEN")
	utils.StringFlagEnv(fs, &cfg.AWSAccessKeyID, "aws-access-key-id", "", "AWS access key ID for Route53 static credentials; uses the default AWS credential chain when omitted", "AWS_ACCESS_KEY_ID")
	utils.StringFlagEnv(fs, &cfg.AWSSecretAccessKey, "aws-secret-access-key", "", "AWS secret access key for Route53 static credentials", "AWS_SECRET_ACCESS_KEY")
	utils.StringFlagEnv(fs, &cfg.AWSSessionToken, "aws-session-token", "", "AWS session token for Route53 temporary credentials", "AWS_SESSION_TOKEN")
	utils.StringFlagEnv(fs, &cfg.AWSRegion, "aws-region", "", "AWS region for Route53 and Route53-backed DNS-01; defaults to us-east-1 when unset", "AWS_REGION", "AWS_DEFAULT_REGION")
	utils.StringFlagEnv(fs, &cfg.AWSHostedZoneID, "aws-hosted-zone-id", "", "explicit Route53 hosted zone ID override", "AWS_HOSTED_ZONE_ID")

	if err := utils.ParseFlagSet(fs, args, printRootUsage); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := utils.RequireNoArgs(fs.Args(), "relay-server"); err != nil {
		printRootUsage(os.Stderr)
		return err
	}

	log.Info().
		Str("release_version", types.ReleaseVersion).
		Str("portal_url", cfg.PortalURL).
		Str("admin_settings_path", cfg.AdminSettingsPath).
		Bool("landing_page_enabled", cfg.LandingPageEnabled).
		Bool("discovery_enabled", cfg.DiscoveryEnabled).
		Bool("wireguard_enabled", strings.TrimSpace(cfg.WireGuardPrivateKey) != "").
		Bool("udp_enabled", cfg.UDPPortCount > 0).
		Msg("configured relay server")

	ctx, stop := utils.SignalContext()
	defer stop()

	return runServer(ctx, cfg)
}

func runServer(ctx context.Context, cfg relayServerConfig) error {
	bootstraps, err := utils.ResolvePortalRelayURLs(ctx, utils.SplitCSV(cfg.Bootstraps), cfg.DiscoveryEnabled)
	if err != nil {
		return fmt.Errorf("resolve discovery bootstraps: %w", err)
	}

	server, err := portal.NewServer(portal.ServerConfig{
		PortalURL:           cfg.PortalURL,
		OwnerPrivateKey:     cfg.OwnerPrivateKey,
		Bootstraps:          bootstraps,
		WireGuardPrivateKey: cfg.WireGuardPrivateKey,
		DiscoveryPort:       cfg.DiscoveryPort,
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
		APIPort:           cfg.APIPort,
		SNIPort:           cfg.SNIPort,
		TrustedProxyCIDRs: cfg.TrustedProxyCIDRs,
		TrustProxyHeaders: cfg.TrustProxyHeaders,
		DiscoveryEnabled:  cfg.DiscoveryEnabled,
		UDPPortCount:      cfg.UDPPortCount,
	})
	if err != nil {
		return fmt.Errorf("create relay server: %w", err)
	}

	frontend, err := NewFrontend(server, cfg.AdminSecretKey, cfg.AdminSettingsPath, cfg.LandingPageEnabled)
	if err != nil {
		return fmt.Errorf("create frontend: %w", err)
	}

	if err := server.Start(ctx, frontend.Handler()); err != nil {
		return fmt.Errorf("start relay server: %w", err)
	}

	rootHost := server.RootHost()
	logEvent := log.Info().
		Str("api_addr", utils.HostPortOrLoopback(server.APIAddr())).
		Str("sni_addr", server.SNIAddr()).
		Str("root_host", rootHost).
		Str("acme_dns_provider", cfg.ACMEDNSProvider).
		Bool("discovery_enabled", server.DiscoveryEnabled()).
		Bool("wireguard_enabled", strings.TrimSpace(cfg.WireGuardPrivateKey) != "").
		Bool("udp_enabled", cfg.UDPPortCount > 0).
		Bool("acme_enabled", !strings.HasSuffix(rootHost, "localhost") && rootHost != "127.0.0.1" && rootHost != "::1")
	if quicAddr := server.QUICTunnelAddr(); quicAddr != "" {
		logEvent = logEvent.Str("internal_quic_tunnel_addr", quicAddr)
	}
	logEvent.Msg("relay server started")

	return server.Wait()
}

func runHelpCommand(args []string) error {
	switch len(args) {
	case 0:
		printRootUsage(os.Stdout)
		return nil
	case 1:
		switch strings.TrimSpace(args[0]) {
		case "", "help", "-h", "--help", "serve":
			printRootUsage(os.Stdout)
			return nil
		default:
			printRootUsage(os.Stderr)
			return fmt.Errorf("unknown help topic %q", strings.TrimSpace(args[0]))
		}
	default:
		printRootUsage(os.Stderr)
		return errors.New("only one help topic is supported")
	}
}

func printRootUsage(w io.Writer) {
	utils.WriteCommandUsage(w,
		[]string{
			"relay-server [flags]",
			"relay-server serve [flags]",
			"relay-server help",
		},
		[]string{
			"relay-server",
			"relay-server serve",
			"relay-server --portal-url https://portal.example.com",
			"relay-server --discovery --udp-port-count 100",
			"relay-server --landing-page-enabled",
			"relay-server help",
		},
	)
}
