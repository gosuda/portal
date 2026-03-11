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

	"github.com/gosuda/portal/v2/portal"
	"github.com/gosuda/portal/v2/portal/acme"
	"github.com/gosuda/portal/v2/portal/admin"
	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/types"
)

func runServer(cfg relayServerConfig) error {
	logger := log.With().Str("component", "relay-server").Logger()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(cfg.Bootstraps) > 0 && cfg.PortalURL == "" {
		cfg.PortalURL = cfg.Bootstraps[0]
	}
	rootHost := portal.PortalRootHost(cfg.PortalURL)
	apiListenAddr := fmt.Sprintf(":%d", cfg.APIPort)
	sniListenAddr := fmt.Sprintf(":%d", cfg.SNIPort)
	trustedProxyCIDRs, err := policy.ParseTrustedProxyCIDRs(cfg.TrustedProxyCIDRs)
	if err != nil {
		return fmt.Errorf("parse trusted proxy cidrs: %w", err)
	}
	policy.SetTrustedProxyCIDRs(trustedProxyCIDRs)

	server, err := portal.NewServer(portal.ServerConfig{
		PortalURL: cfg.PortalURL,
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
		TrustProxyHeaders: cfg.TrustProxyHeaders,
	})
	if err != nil {
		return fmt.Errorf("create relay server: %w", err)
	}

	frontend := NewFrontend(cfg.PortalURL)
	adminHandler := admin.NewHandler(cfg.PortalURL, cfg.AdminSecretKey, "admin_settings.json", cfg.TrustProxyHeaders, func(w http.ResponseWriter, r *http.Request, appPath string) {
		frontend.ServeAppStatic(w, r, appPath)
	})
	frontend.Bind(server)
	adminHandler.Bind(server)
	if loadErr := adminHandler.LoadSettings(); loadErr != nil {
		logger.Warn().Err(loadErr).Msg("load admin settings")
	}

	if err := server.Start(ctx, newAPIMux(frontend, adminHandler, cfg)); err != nil {
		return fmt.Errorf("start relay server: %w", err)
	}

	logger.Info().
		Str("api_addr", portal.HostPortOrLoopback(server.APIAddr())).
		Str("sni_addr", server.SNIAddr()).
		Str("root_host", rootHost).
		Str("acme_dns_provider", cfg.ACMEDNSProvider).
		Bool("acme_enabled", !strings.HasSuffix(rootHost, "localhost") && rootHost != "127.0.0.1" && rootHost != "::1").
		Msg("relay server started")

	return server.Wait()
}

func newAPIMux(frontend *Frontend, adminHandler *admin.Handler, cfg relayServerConfig) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		frontend.ServeAppStatic(w, r, "")
	})
	mux.HandleFunc(types.PathApp, func(w http.ResponseWriter, r *http.Request) {
		frontend.ServeAppStatic(w, r, "")
	})
	mux.HandleFunc(types.PathAppPrefix, func(w http.ResponseWriter, r *http.Request) {
		frontend.ServeAppStatic(w, r, strings.TrimPrefix(strings.TrimSpace(r.URL.Path), types.PathAppPrefix))
	})
	mux.HandleFunc(types.PathAssetsPrefix, func(w http.ResponseWriter, r *http.Request) {
		frontend.ServeAsset(w, r, strings.TrimPrefix(r.URL.Path, "/"), "")
	})
	for _, assetPath := range frontendRootAssetPaths() {
		mux.HandleFunc(assetPath, func(w http.ResponseWriter, r *http.Request) {
			frontend.ServeAsset(w, r, strings.TrimPrefix(assetPath, "/"), "")
		})
	}

	mux.HandleFunc(types.PathAdmin, adminHandler.HandleRequest)
	mux.HandleFunc(types.PathAdminPrefix, adminHandler.HandleRequest)
	mux.HandleFunc(types.PathTunnel, func(w http.ResponseWriter, r *http.Request) {
		serveTunnelScript(w, r, cfg.PortalURL)
	})
	mux.HandleFunc(types.PathTunnelBinPrefix, serveTunnelBinary)

	return mux
}

func frontendRootAssetPaths() []string {
	return []string{
		"/favicon.ico",
		"/favicon.svg",
		"/favicon-96x96.png",
		"/apple-touch-icon.png",
		"/web-app-manifest-192x192.png",
		"/web-app-manifest-512x512.png",
		"/portal.jpg",
	}
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
