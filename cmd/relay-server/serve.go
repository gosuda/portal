package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/acme"
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

	frontend := NewFrontend(cfg.PortalURL)
	admin := NewAdmin(cfg.AdminSecretKey, cfg.TrustProxyHeaders, frontend)

	server, err := portal.NewServer(portal.ServerConfig{
		PortalURL:        cfg.PortalURL,
		APIListenAddr:    apiListenAddr,
		SNIListenAddr:    sniListenAddr,
		RootHost:         rootHost,
		RootFallbackAddr: loopbackAddr(apiListenAddr),
		APITLS: portal.TLSMaterialConfig{
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
		Str("api_addr", loopbackAddr(server.APIAddr())).
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
			case r.URL.Path == "/healthz":
				base.ServeHTTP(w, r)
			case isFrontendRootAssetPath(r.URL.Path):
				frontend.ServeAsset(w, r, strings.TrimPrefix(r.URL.Path, "/"), "")
			case hasPathPrefix(r.URL.Path, "/assets/"):
				frontend.ServeAsset(w, r, strings.TrimPrefix(r.URL.Path, "/"), "")
			case r.URL.Path == "/" || r.URL.Path == "/app" || r.URL.Path == "/app/":
				frontend.ServeAppStatic(w, r, "")
			case hasPathPrefix(r.URL.Path, "/app/"):
				frontend.ServeAppStatic(w, r, trimPathPrefix(r.URL.Path, "/app/"))
			case r.URL.Path == "/admin" || r.URL.Path == "/admin/":
				admin.HandleAdminRequest(w, r)
			case hasPathPrefix(r.URL.Path, "/admin/"):
				admin.HandleAdminRequest(w, r)
			case r.URL.Path == "/tunnel":
				serveTunnelScript(w, r, cfg.PortalURL)
			case hasPathPrefix(r.URL.Path, "/tunnel/bin/"):
				serveTunnelBinary(w, r)
			default:
				base.ServeHTTP(w, r)
			}
		})
	}
}

func isFrontendRootAssetPath(requestPath string) bool {
	switch requestPath {
	case "/favicon.ico",
		"/favicon.svg",
		"/favicon-96x96.png",
		"/apple-touch-icon.png",
		"/web-app-manifest-192x192.png",
		"/web-app-manifest-512x512.png",
		"/portal.jpg":
		return true
	default:
		return false
	}
}

func loopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
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
