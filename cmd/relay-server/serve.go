package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/acme"
	"gosuda.org/portal/portal/keyless"
	"gosuda.org/portal/types"
)

const (
	pathFaviconICO           = "/favicon.ico"
	pathFaviconSVG           = "/favicon.svg"
	pathFavicon96PNG         = "/favicon-96x96.png"
	pathAppleTouchIconPNG    = "/apple-touch-icon.png"
	pathWebAppManifest192PNG = "/web-app-manifest-192x192.png"
	pathWebAppManifest512PNG = "/web-app-manifest-512x512.png"
	pathPortalJPG            = "/portal.jpg"
)

func runServer(cfg relayServerConfig) error {
	logger := log.With().Str("component", "relay-server").Logger()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(cfg.Bootstraps) > 0 && cfg.PortalURL == "" {
		cfg.PortalURL = cfg.Bootstraps[0]
	}
	rootHost := portal.PortalRootHost(cfg.PortalURL)
	apiListenAddr := fmt.Sprintf(":%d", cfg.AdminPort)
	sniListenAddr := fmt.Sprintf(":%d", cfg.SNIPort)

	acmeManager, err := acme.NewManager(acme.Config{
		BaseDomain:      rootHost,
		KeyDir:          cfg.KeylessDir,
		CloudflareToken: cfg.CloudflareToken,
	})
	if err != nil {
		return fmt.Errorf("create acme manager: %w", err)
	}

	certFile, keyFile, err := acmeManager.EnsureCertificate(ctx)
	if err != nil {
		return fmt.Errorf("ensure relay certificate: %w", err)
	}
	signer, err := keyless.NewSigner(keyFile)
	if err != nil {
		return fmt.Errorf("create keyless signer: %w", err)
	}

	frontend := NewFrontend(cfg.PortalURL)
	admin := NewAdmin(cfg.AdminSecretKey, cfg.TrustProxyHeaders, frontend)

	server, err := portal.NewServer(portal.ServerConfig{
		PortalURL:        cfg.PortalURL,
		APIListenAddr:    apiListenAddr,
		SNIListenAddr:    sniListenAddr,
		RootHost:         rootHost,
		RootFallbackAddr: portal.HostPortOrLoopback(apiListenAddr),
		KeylessSignerHandler: func() http.Handler {
			if signer == nil {
				return nil
			}
			return signer.Handler()
		}(),
		APITLS: keyless.TLSMaterialConfig{
			CertPEM: mustRead(certFile),
			KeyPEM:  mustRead(keyFile),
		},
		APIHandlerWrapper: serveAPI(frontend, admin, cfg),
	})
	if err != nil {
		return fmt.Errorf("create relay server: %w", err)
	}

	frontend.Bind(server)
	admin.Bind(server)

	if err := server.Start(ctx); err != nil {
		return fmt.Errorf("start relay server: %w", err)
	}
	acmeManager.Start(ctx)
	defer acmeManager.Stop()

	logger.Info().
		Str("api_addr", portal.HostPortOrLoopback(server.APIAddr())).
		Str("sni_addr", server.SNIAddr()).
		Str("root_host", rootHost).
		Bool("acme_enabled", !strings.HasSuffix(rootHost, "localhost") && rootHost != "127.0.0.1" && rootHost != "::1").
		Msg("relay server started")

	return server.Wait()
}

func serveAPI(frontend *Frontend, admin *Admin, cfg relayServerConfig) func(http.Handler) http.Handler {
	return func(base http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case isRelayControlPlanePath(r.URL.Path):
				base.ServeHTTP(w, r)
			case r.URL.Path == types.PathV1Sign:
				base.ServeHTTP(w, r)
			case r.URL.Path == types.PathHealthz:
				base.ServeHTTP(w, r)
			case isFrontendRootAssetPath(r.URL.Path):
				frontend.ServeAsset(w, r, strings.TrimPrefix(r.URL.Path, "/"), "")
			case strings.HasPrefix(strings.TrimSpace(r.URL.Path), types.PathAssetsPrefix):
				frontend.ServeAsset(w, r, strings.TrimPrefix(r.URL.Path, "/"), "")
			case r.URL.Path == types.PathRoot || r.URL.Path == types.PathApp || r.URL.Path == types.PathAppPrefix:
				frontend.ServeAppStatic(w, r, "")
			case strings.HasPrefix(strings.TrimSpace(r.URL.Path), types.PathAppPrefix):
				frontend.ServeAppStatic(w, r, strings.TrimPrefix(strings.TrimSpace(r.URL.Path), types.PathAppPrefix))
			case r.URL.Path == types.PathAdmin || r.URL.Path == types.PathAdminPrefix:
				admin.HandleAdminRequest(w, r)
			case strings.HasPrefix(strings.TrimSpace(r.URL.Path), types.PathAdminPrefix):
				admin.HandleAdminRequest(w, r)
			case r.URL.Path == types.PathTunnel:
				serveTunnelScript(w, r, cfg.PortalURL)
			case strings.HasPrefix(strings.TrimSpace(r.URL.Path), types.PathTunnelBinPrefix):
				serveTunnelBinary(w, r)
			default:
				base.ServeHTTP(w, r)
			}
		})
	}
}

func isFrontendRootAssetPath(requestPath string) bool {
	switch requestPath {
	case pathFaviconICO,
		pathFaviconSVG,
		pathFavicon96PNG,
		pathAppleTouchIconPNG,
		pathWebAppManifest192PNG,
		pathWebAppManifest512PNG,
		pathPortalJPG:
		return true
	default:
		return false
	}
}

func mustRead(path string) []byte {
	if path == "" {
		log.Fatal().Msg("missing required PEM path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatal().Err(err).Str("path", path).Msg("read pem file")
	}
	return data
}

func parseURLs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func isRelayControlPlanePath(path string) bool {
	switch strings.TrimSpace(path) {
	case types.PathSDKRegister, types.PathSDKConnect, types.PathSDKRenew, types.PathSDKUnregister, types.PathSDKDomain:
		return true
	}
	return strings.HasPrefix(path, types.PathSDKPrefix)
}
