package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	keylessserver "keyless_tls/relay/server"
	keylesssigner "keyless_tls/relay/signer"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/utils/sni"
)

var (
	// Server
	flagPort           int
	flagAdminSecretKey string
	flagNoIndex        bool
	flagLeaseBPS       int

	// Portal
	flagPortalURL  string
	flagBootstraps []string

	// TLS
	flagSNIPort string

	// Keyless signer
	flagSignerListenAddr    string
	flagSignerEndpoint      string
	flagSignerTLSServerName string
	flagSignerCertChainFile string
	flagSignerTLSCertFile   string
	flagSignerTLSKeyFile    string
	flagSignerSigningKey    string
	flagSignerKeyID         string
	flagSignerRootCAFile    string
	flagSignerEnableMTLS    bool
	flagSignerClientCAFile  string
	flagSignerClientCert    string
	flagSignerClientKey     string
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	defaultPortalURL := strings.TrimSuffix(os.Getenv("PORTAL_URL"), "/")
	if defaultPortalURL == "" {
		// Prefer explicit scheme for localhost so downstream URL building is unambiguous
		defaultPortalURL = "http://localhost:4017"
	}
	defaultBootstraps := os.Getenv("BOOTSTRAP_URIS")
	if defaultBootstraps == "" {
		defaultBootstraps = defaultBootstrapFrom(defaultPortalURL)
	}
	defaultSNIPort := os.Getenv("SNI_PORT")
	if defaultSNIPort == "" {
		defaultSNIPort = ":443"
	}
	defaultSignerServerName := portalHostname(defaultPortalURL)
	defaultSignerEndpoint := os.Getenv("SIGNER_ENDPOINT")
	if defaultSignerEndpoint == "" {
		defaultSignerEndpoint = defaultSignerEndpointFrom(defaultPortalURL)
	}

	// Server flags
	flag.IntVar(&flagPort, "port", 4017, "HTTP server port")
	flag.StringVar(&flagAdminSecretKey, "admin-secret-key", os.Getenv("ADMIN_SECRET_KEY"), "admin auth secret (env: ADMIN_SECRET_KEY)")
	flag.BoolVar(&flagNoIndex, "noindex", os.Getenv("NOINDEX") == "true", "disallow crawlers (env: NOINDEX)")
	flag.IntVar(&flagLeaseBPS, "lease-bps", 0, "bytes-per-second limit per lease (0=unlimited)")

	// Portal flags
	var flagBootstrapsCSV string
	flag.StringVar(&flagPortalURL, "portal-url", defaultPortalURL, "portal base URL (env: PORTAL_URL)")
	flag.StringVar(&flagBootstrapsCSV, "bootstraps", defaultBootstraps, "bootstrap URIs, comma-separated (env: BOOTSTRAP_URIS)")

	// TLS flags
	flag.StringVar(&flagSNIPort, "sni-port", defaultSNIPort, "SNI router port (env: SNI_PORT)")

	// Keyless signer flags
	flag.StringVar(&flagSignerListenAddr, "signer-listen", envOrDefault("SIGNER_LISTEN_ADDR", ":9443"), "signer HTTPS listen address (env: SIGNER_LISTEN_ADDR)")
	flag.StringVar(&flagSignerEndpoint, "signer-endpoint", defaultSignerEndpoint, "public signer endpoint for tunnel SDK (env: SIGNER_ENDPOINT)")
	flag.StringVar(&flagSignerTLSServerName, "signer-server-name", envOrDefault("SIGNER_SERVER_NAME", defaultSignerServerName), "TLS server name for signer verification (env: SIGNER_SERVER_NAME)")
	flag.StringVar(&flagSignerCertChainFile, "signer-cert-chain", envOrDefault("SIGNER_CERT_CHAIN_FILE", os.Getenv("SIGNER_TLS_CERT_FILE")), "certificate chain PEM distributed to tunnel SDK (env: SIGNER_CERT_CHAIN_FILE)")
	flag.StringVar(&flagSignerTLSCertFile, "signer-tls-cert", os.Getenv("SIGNER_TLS_CERT_FILE"), "signer TLS certificate file path (env: SIGNER_TLS_CERT_FILE)")
	flag.StringVar(&flagSignerTLSKeyFile, "signer-tls-key", os.Getenv("SIGNER_TLS_KEY_FILE"), "signer TLS private key file path (env: SIGNER_TLS_KEY_FILE)")
	flag.StringVar(&flagSignerSigningKey, "signer-sign-key", os.Getenv("SIGNER_SIGNING_KEY_FILE"), "signing private key PEM file path (env: SIGNER_SIGNING_KEY_FILE)")
	flag.StringVar(&flagSignerKeyID, "signer-key-id", envOrDefault("SIGNER_KEY_ID", "portal-wildcard"), "key ID exposed to tunnel SDK (env: SIGNER_KEY_ID)")
	flag.StringVar(&flagSignerRootCAFile, "signer-root-ca", os.Getenv("SIGNER_ROOT_CA_FILE"), "root CA PEM file delivered to tunnel SDK (env: SIGNER_ROOT_CA_FILE)")
	flag.BoolVar(&flagSignerEnableMTLS, "signer-enable-mtls", parseBoolEnv("SIGNER_ENABLE_MTLS"), "require mTLS for signer endpoint (env: SIGNER_ENABLE_MTLS)")
	flag.StringVar(&flagSignerClientCAFile, "signer-client-ca", os.Getenv("SIGNER_CLIENT_CA_FILE"), "client CA PEM file for signer mTLS (env: SIGNER_CLIENT_CA_FILE)")
	flag.StringVar(&flagSignerClientCert, "signer-client-cert", os.Getenv("SIGNER_CLIENT_CERT_FILE"), "client cert PEM delivered to tunnel SDK when mTLS is enabled (env: SIGNER_CLIENT_CERT_FILE)")
	flag.StringVar(&flagSignerClientKey, "signer-client-key", os.Getenv("SIGNER_CLIENT_KEY_FILE"), "client key PEM delivered to tunnel SDK when mTLS is enabled (env: SIGNER_CLIENT_KEY_FILE)")

	flag.Parse()

	flagBootstraps = parseURLs(flagBootstrapsCSV)
	if err := runServer(); err != nil {
		log.Fatal().Err(err).Msg("execute root command")
	}
}

func runServer() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info().
		Str("portal_base_url", flagPortalURL).
		Strs("bootstrap_uris", flagBootstraps).
		Msg("[server] frontend configuration")

	serv := portal.NewRelayServer(flagBootstraps, flagSNIPort, flagPortalURL)

	registry, err := newSDKRegistry(ctx)
	if err != nil {
		return err
	}

	frontend := NewFrontend()
	authManager := manager.NewAuthManager(flagAdminSecretKey)
	admin := NewAdmin(int64(flagLeaseBPS), frontend, authManager)
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
		log.Fatal().Err(err).Msg("[server] Failed to start relay server")
	}
	defer serv.Stop()

	httpSrv := serveHTTP(fmt.Sprintf(":%d", flagPort), serv, admin, frontend, registry, flagNoIndex, stop)

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

func newSDKRegistry(ctx context.Context) (*SDKRegistry, error) {
	config, signerCfg, err := loadKeylessConfig()
	if err != nil {
		return nil, err
	}
	registry := &SDKRegistry{keylessConfig: config}

	if !config.Enabled {
		log.Warn().Str("reason", config.DisabledReason).Msg("[keyless] signer is disabled; TLS tunnels will fail")
		return registry, nil
	}

	store := keylesssigner.NewStaticKeyStore()
	signer, err := keylesssigner.ParsePrivateKeyPEM(config.signingKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse signer signing key: %w", err)
	}
	if err := store.Put(config.KeyID, signer); err != nil {
		return nil, fmt.Errorf("register signer key: %w", err)
	}

	svc := &keylesssigner.Service{Store: store}

	go func() {
		err := keylessserver.ListenAndServe(ctx, keylessserver.Config{
			ListenAddr:    signerCfg.ListenAddr,
			ServerCertPEM: signerCfg.ServerCertPEM,
			ServerKeyPEM:  signerCfg.ServerKeyPEM,
			EnableMTLS:    signerCfg.EnableMTLS,
			ClientCAPEM:   signerCfg.ClientCAPEM,
			SignerService: svc,
		})
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
			log.Error().Err(err).Msg("[keyless] signer server stopped unexpectedly")
		}
	}()

	log.Info().
		Str("listen", signerCfg.ListenAddr).
		Str("endpoint", config.SignerEndpoint).
		Str("key_id", config.KeyID).
		Bool("mtls", config.RequireMTLS).
		Msg("[keyless] signer server started")

	return registry, nil
}

type signerServerConfig struct {
	ListenAddr    string
	ServerCertPEM []byte
	ServerKeyPEM  []byte
	EnableMTLS    bool
	ClientCAPEM   []byte
}

func loadKeylessConfig() (*RelayKeylessConfig, signerServerConfig, error) {
	if strings.TrimSpace(flagSignerTLSCertFile) == "" ||
		strings.TrimSpace(flagSignerTLSKeyFile) == "" ||
		strings.TrimSpace(flagSignerSigningKey) == "" ||
		strings.TrimSpace(flagSignerRootCAFile) == "" ||
		strings.TrimSpace(flagSignerCertChainFile) == "" {
		return &RelayKeylessConfig{Enabled: false, DisabledReason: "missing signer TLS/signing/root CA files"}, signerServerConfig{}, nil
	}

	serverCertPEM, err := os.ReadFile(flagSignerTLSCertFile)
	if err != nil {
		return nil, signerServerConfig{}, fmt.Errorf("read signer TLS cert: %w", err)
	}
	serverKeyPEM, err := os.ReadFile(flagSignerTLSKeyFile)
	if err != nil {
		return nil, signerServerConfig{}, fmt.Errorf("read signer TLS key: %w", err)
	}
	signingKeyPEM, err := os.ReadFile(flagSignerSigningKey)
	if err != nil {
		return nil, signerServerConfig{}, fmt.Errorf("read signer signing key: %w", err)
	}
	rootCAPEM, err := os.ReadFile(flagSignerRootCAFile)
	if err != nil {
		return nil, signerServerConfig{}, fmt.Errorf("read signer root CA: %w", err)
	}
	certChainPEM, err := os.ReadFile(flagSignerCertChainFile)
	if err != nil {
		return nil, signerServerConfig{}, fmt.Errorf("read signer cert chain: %w", err)
	}

	var clientCAPEM []byte
	var clientCertPEM []byte
	var clientKeyPEM []byte
	if flagSignerEnableMTLS {
		if strings.TrimSpace(flagSignerClientCAFile) == "" {
			return nil, signerServerConfig{}, fmt.Errorf("signer mTLS enabled but signer-client-ca is empty")
		}
		clientCAPEM, err = os.ReadFile(flagSignerClientCAFile)
		if err != nil {
			return nil, signerServerConfig{}, fmt.Errorf("read signer client CA: %w", err)
		}
		if strings.TrimSpace(flagSignerClientCert) != "" && strings.TrimSpace(flagSignerClientKey) != "" {
			clientCertPEM, err = os.ReadFile(flagSignerClientCert)
			if err != nil {
				return nil, signerServerConfig{}, fmt.Errorf("read signer client cert: %w", err)
			}
			clientKeyPEM, err = os.ReadFile(flagSignerClientKey)
			if err != nil {
				return nil, signerServerConfig{}, fmt.Errorf("read signer client key: %w", err)
			}
		} else {
			log.Warn().Msg("[keyless] signer mTLS enabled but client cert/key is missing; tunnels must provide credentials out-of-band")
		}
	}

	config := &RelayKeylessConfig{
		Enabled:          true,
		CertChainPEM:     certChainPEM,
		SignerEndpoint:   strings.TrimSpace(flagSignerEndpoint),
		SignerServerName: strings.TrimSpace(flagSignerTLSServerName),
		KeyID:            strings.TrimSpace(flagSignerKeyID),
		RootCAPEM:        rootCAPEM,
		RequireMTLS:      flagSignerEnableMTLS,
		ClientCertPEM:    clientCertPEM,
		ClientKeyPEM:     clientKeyPEM,
		signingKeyPEM:    signingKeyPEM,
	}
	if config.SignerEndpoint == "" || config.SignerServerName == "" || config.KeyID == "" {
		return nil, signerServerConfig{}, fmt.Errorf("keyless signer config requires signer-endpoint, signer-server-name, and signer-key-id")
	}

	return config, signerServerConfig{
		ListenAddr:    strings.TrimSpace(flagSignerListenAddr),
		ServerCertPEM: serverCertPEM,
		ServerKeyPEM:  serverKeyPEM,
		EnableMTLS:    flagSignerEnableMTLS,
		ClientCAPEM:   clientCAPEM,
	}, nil
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func parseBoolEnv(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func portalHostname(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "localhost"
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || strings.TrimSpace(u.Hostname()) == "" {
		return "localhost"
	}
	return strings.TrimSpace(u.Hostname())
}

func defaultSignerEndpointFrom(portalURL string) string {
	host := portalHostname(portalURL)
	if host == "" {
		host = "localhost"
	}
	return net.JoinHostPort(host, "9443")
}
