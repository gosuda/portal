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
	TCPPortCount        int
	LandingPageEnabled  bool
	Bootstraps          string
	DiscoveryEnabled    bool
	IdentityPath        string
	WireGuardPrivateKey string
	DiscoveryPort       int
	WireGuardEndpoint   string
	AdminSecretKey      string
	TrustProxyHeaders   bool
	TrustedProxyCIDRs   string
	AdminSettingsPath   string
	KeylessDir          string
	ACMEDNSProvider     string
	ENSGaslessEnabled   bool
	CloudflareToken     string
	GCPProjectID        string
	GCPManagedZone      string
	AWSAccessKeyID      string
	AWSSecretAccessKey  string
	AWSSessionToken     string
	AWSRegion           string
	AWSHostedZoneID     string
	AWSDNSSECKMSKeyARN  string
	DNSSECKSKName       string
}

func runServeCommand(args []string) error {
	cfg := relayServerConfig{}
	fs := utils.NewFlagSet("relay-server", printRootUsage)

	utils.StringFlagEnv(fs, &cfg.PortalURL, "portal-url", "https://localhost:4017", "portal base URL", "PORTAL_URL")
	utils.IntFlagEnv(fs, &cfg.APIPort, "api-port", 4017, utils.ParsePortNumber, "Admin/API server port", "API_PORT")
	utils.IntFlagEnv(fs, &cfg.SNIPort, "sni-port", 443, utils.ParsePortNumber, "TCP SNI router port number", "SNI_PORT")
	utils.IntFlagEnv(fs, &cfg.UDPPortCount, "udp-port-count", 0, utils.ParseNonNegativeInt, "Number of UDP ports to allocate for leases, starting at port 50000 (0=disabled)", "UDP_PORT_COUNT")
	utils.IntFlagEnv(fs, &cfg.TCPPortCount, "tcp-port-count", 0, utils.ParseNonNegativeInt, "Number of TCP ports to allocate for raw TCP leases, starting at port 40000 (0=disabled)", "TCP_PORT_COUNT")
	utils.BoolFlagEnv(fs, &cfg.LandingPageEnabled, "landing-page-enabled", false, "enable landing page by default when no admin setting has been saved yet", "LANDING_PAGE_ENABLED")
	utils.StringFlagEnv(fs, &cfg.Bootstraps, "bootstraps", "", "additional bootstrap relay API URLs used for discovery expansion", "BOOTSTRAPS")
	utils.BoolFlagEnv(fs, &cfg.DiscoveryEnabled, "discovery", false, "serve relay discovery endpoints and poll discovery peers", "DISCOVERY")
	utils.StringFlagEnv(fs, &cfg.IdentityPath, "identity-path", "identity.json", "relay identity json file path", "IDENTITY_PATH")
	utils.StringFlagEnv(fs, &cfg.WireGuardPrivateKey, "wireguard-private-key", "", "wireguard private key for relay peer overlay", "WIREGUARD_PRIVATE_KEY")
	utils.IntFlagEnv(fs, &cfg.DiscoveryPort, "discovery-port", 0, utils.ParsePortNumber, "public UDP listen port advertised for relay-peer discovery overlay (defaults to 51820 when wireguard is enabled)", "DISCOVERY_PORT")
	utils.StringFlagEnv(fs, &cfg.WireGuardEndpoint, "wireguard-endpoint", "", "explicit public WireGuard endpoint advertised for relay peer overlay (host:port or ip:port); defaults to PORTAL_URL host + DISCOVERY_PORT when empty", "WIREGUARD_ENDPOINT")
	utils.StringFlagEnv(fs, &cfg.AdminSecretKey, "admin-secret-key", "", "admin auth secret", "ADMIN_SECRET_KEY")
	utils.BoolFlagEnv(fs, &cfg.TrustProxyHeaders, "trust-proxy-headers", false, "trust X-Forwarded-* and X-Real-IP headers from trusted proxies", "TRUST_PROXY_HEADERS")
	utils.StringFlagEnv(fs, &cfg.TrustedProxyCIDRs, "trusted-proxy-cidrs", "", "trusted proxy CIDR allowlist for forwarded headers, comma-separated; defaults to private/loopback proxy ranges when trust-proxy-headers is enabled", "TRUSTED_PROXY_CIDRS")

	utils.StringFlagEnv(fs, &cfg.KeylessDir, "keyless-dir", "./.portal-certs", "directory path for relay keyless materials", "KEYLESS_DIR")
	utils.StringFlagEnv(fs, &cfg.AdminSettingsPath, "admin-settings-path", "admin_settings.json", "admin settings file path", "ADMIN_SETTINGS_PATH")
	utils.StringFlagEnv(fs, &cfg.ACMEDNSProvider, "acme-dns-provider", "", "ACME DNS provider for managed DNS-01/A-record sync and ENS gasless DNSSEC/TXT automation (cloudflare|gcloud|route53); leave empty to use manual fullchain.pem/privatekey.pem from KEYLESS_DIR", "ACME_DNS_PROVIDER")
	utils.BoolFlagEnv(fs, &cfg.ENSGaslessEnabled, "ens-gasless-enabled", false, "enable ENS gasless DNS import automation for the managed DNS zone and lease hostnames", "ENS_GASLESS_ENABLED")
	utils.StringFlagEnv(fs, &cfg.CloudflareToken, "cloudflare-token", "", "Cloudflare DNS API token (required when acme-dns-provider=cloudflare)", "CLOUDFLARE_TOKEN")
	utils.StringFlagEnv(fs, &cfg.GCPProjectID, "gcp-project-id", "", "Google Cloud project id for Cloud DNS automation; auto-detected from ADC or GCE metadata when omitted", "GCP_PROJECT_ID", "GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT", "GCE_PROJECT")
	utils.StringFlagEnv(fs, &cfg.GCPManagedZone, "gcp-managed-zone", "", "explicit Google Cloud DNS managed zone name or numeric ID override", "GCP_MANAGED_ZONE", "GCP_ZONE", "GCE_ZONE_ID")
	utils.StringFlagEnv(fs, &cfg.AWSAccessKeyID, "aws-access-key-id", "", "AWS access key ID for Route53 static credentials; uses the default AWS credential chain when omitted", "AWS_ACCESS_KEY_ID")
	utils.StringFlagEnv(fs, &cfg.AWSSecretAccessKey, "aws-secret-access-key", "", "AWS secret access key for Route53 static credentials", "AWS_SECRET_ACCESS_KEY")
	utils.StringFlagEnv(fs, &cfg.AWSSessionToken, "aws-session-token", "", "AWS session token for Route53 temporary credentials", "AWS_SESSION_TOKEN")
	utils.StringFlagEnv(fs, &cfg.AWSRegion, "aws-region", "", "AWS region for Route53 and Route53-backed DNS-01; defaults to us-east-1 when unset", "AWS_REGION", "AWS_DEFAULT_REGION")
	utils.StringFlagEnv(fs, &cfg.AWSHostedZoneID, "aws-hosted-zone-id", "", "explicit Route53 hosted zone ID override", "AWS_HOSTED_ZONE_ID")
	utils.StringFlagEnv(fs, &cfg.AWSDNSSECKMSKeyARN, "aws-dnssec-kms-key-arn", "", "AWS KMS key ARN used to create a Route53 DNSSEC key-signing key when needed", "AWS_DNSSEC_KMS_KEY_ARN")
	utils.StringFlagEnv(fs, &cfg.DNSSECKSKName, "dnssec-ksk-name", "", "optional key-signing key name override for Route53 DNSSEC automation", "DNSSEC_KSK_NAME")

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
		Str("identity_path", cfg.IdentityPath).
		Str("admin_settings_path", cfg.AdminSettingsPath).
		Bool("landing_page_enabled", cfg.LandingPageEnabled).
		Bool("discovery_enabled", cfg.DiscoveryEnabled).
		Str("acme_dns_provider", cfg.ACMEDNSProvider).
		Bool("ens_gasless_enabled", cfg.ENSGaslessEnabled).
		Bool("wireguard_enabled", strings.TrimSpace(cfg.WireGuardPrivateKey) != "").
		Bool("udp_enabled", cfg.UDPPortCount > 0).
		Bool("tcp_port_enabled", cfg.TCPPortCount > 0).
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
		IdentityPath:        cfg.IdentityPath,
		Bootstraps:          bootstraps,
		WireGuardPrivateKey: cfg.WireGuardPrivateKey,
		DiscoveryPort:       cfg.DiscoveryPort,
		WireGuardEndpoint:   cfg.WireGuardEndpoint,
		ACME: acme.Config{
			KeyDir:             cfg.KeylessDir,
			DNSProvider:        cfg.ACMEDNSProvider,
			ENSGaslessEnabled:  cfg.ENSGaslessEnabled,
			CloudflareToken:    cfg.CloudflareToken,
			GCPProjectID:       cfg.GCPProjectID,
			GCPManagedZone:     cfg.GCPManagedZone,
			AWSAccessKeyID:     cfg.AWSAccessKeyID,
			AWSSecretAccessKey: cfg.AWSSecretAccessKey,
			AWSSessionToken:    cfg.AWSSessionToken,
			AWSRegion:          cfg.AWSRegion,
			AWSHostedZoneID:    cfg.AWSHostedZoneID,
			AWSKMSKeyARN:       cfg.AWSDNSSECKMSKeyARN,
			DNSSECKSKName:      cfg.DNSSECKSKName,
		},
		APIPort:           cfg.APIPort,
		SNIPort:           cfg.SNIPort,
		TrustedProxyCIDRs: cfg.TrustedProxyCIDRs,
		TrustProxyHeaders: cfg.TrustProxyHeaders,
		DiscoveryEnabled:  cfg.DiscoveryEnabled,
		UDPPortCount:      cfg.UDPPortCount,
		TCPPortCount:      cfg.TCPPortCount,
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
