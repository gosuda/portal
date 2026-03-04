package main

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/keyless"
	"gosuda.org/portal/types"
)

const defaultHTTPSPort = "443"

//go:embed dist/*
var distFS embed.FS

// serveAPI builds the admin/API mux and returns the server.
func serveAPI(addr string, serv *portal.RelayServer, admin *Admin, frontend *Frontend, cfg relayServerConfig, cancel context.CancelFunc) *http.Server {
	if addr == "" {
		addr = ":0"
	}

	// Create app UI mux
	appMux := http.NewServeMux()

	// Serve favicons (ico/png/svg) from dist/app
	frontend.ServeAsset(appMux, "/favicon.ico", "favicon.ico", "image/x-icon")
	frontend.ServeAsset(appMux, "/favicon.png", "favicon.png", "image/png")
	frontend.ServeAsset(appMux, "/favicon.svg", "favicon.svg", "image/svg+xml")

	// Portal app assets (JS, CSS, etc.) - served from /app/
	appMux.HandleFunc(types.PathAppPrefix, func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, types.PathAppPrefix)
		frontend.ServeAppStatic(w, r, p, serv)
	})

	// Tunnel installer script and binaries
	appMux.HandleFunc(types.PathTunnelScript, func(w http.ResponseWriter, r *http.Request) {
		serveTunnelScript(w, r, cfg.PortalURL)
	})
	appMux.HandleFunc(types.PathTunnelBinary, func(w http.ResponseWriter, r *http.Request) {
		serveTunnelBinary(w, r)
	})

	// SDK registry API for /sdk/* endpoints
	var sdkIPManager *manager.IPManager
	if admin != nil {
		sdkIPManager = admin.GetIPManager()
	}
	registry := &SDKRegistry{
		ipManager:         sdkIPManager,
		portalURL:         cfg.PortalURL,
		trustProxyHeaders: cfg.TrustProxyHeaders,
	}
	appMux.HandleFunc(types.PathSDKPrefix, func(w http.ResponseWriter, r *http.Request) {
		registry.HandleSDKRequest(w, r, serv)
	})

	// Keyless signer endpoint.
	appMux.HandleFunc(types.PathKeylessSign, func(w http.ResponseWriter, r *http.Request) {
		handleKeylessSign(w, r, serv.GetKeylessSigner())
	})

	// App UI index page - serve React frontend with SSR (delegates to serveAppStatic)
	appMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// serveAppStatic handles both "/" and 404 fallback with SSR
		p := strings.TrimPrefix(r.URL.Path, "/")
		frontend.ServeAppStatic(w, r, p, serv)
	})

	appMux.HandleFunc(types.PathHealthz, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("{\"status\":\"ok\"}")); err != nil {
			log.Debug().Err(err).Msg("[healthz] failed to write response")
		}
	})

	// Admin API
	appMux.HandleFunc(types.PathAdminPrefix+"/", func(w http.ResponseWriter, r *http.Request) {
		admin.HandleAdminRequest(w, r, serv)
	})

	// Create the main handler
	appDomain := types.DefaultAppPattern(cfg.PortalURL)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handle subdomain requests
		if types.IsSubdomain(appDomain, r.Host) {
			log.Debug().
				Str("host", r.Host).
				Str("url", r.URL.String()).
				Msg("[server] handling subdomain request")
			// TLS-enabled subdomains should terminate on SNI passthrough.
			// Redirect only insecure requests; secure requests here would loop.
			if !isSecureRequestWithPolicy(r, cfg.TrustProxyHeaders) {
				log.Debug().Str("host", r.Host).Msg("[server] redirecting to HTTPS")
				redirectToHTTPS(w, r, serv.GetSNIRouter().GetAddr())
				return
			}

			log.Warn().Str("host", r.Host).Msg("[server] tls subdomain reached admin listener without SNI route")
			http.Error(w, "tls-enabled subdomain must be served via SNI route", http.StatusMisdirectedRequest)
			return
		}
		appMux.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		TLSNextProto:      make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}
	acmeManager := serv.GetACMEManager()
	rootHost := types.PortalRootHost(cfg.PortalURL)
	srv.TLSConfig = &tls.Config{
		ClientAuth: tls.RequestClientCert,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			serverName := strings.TrimSpace(strings.ToLower(hello.ServerName))
			if serverName != "" && !strings.EqualFold(serverName, rootHost) {
				return nil, fmt.Errorf("acme certificate is only served for portal root host %q", rootHost)
			}
			certFile, keyFile := acmeManager.TLSFiles()
			cert, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				return nil, fmt.Errorf("load acme certificate: %w", err)
			}
			return &cert, nil
		},
	}

	go func() {
		log.Info().Str("addr", addr).Msg("[server] https api enabled via ACME")
		err := srv.ListenAndServeTLS("", "")
		if err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("[server] http error")
			cancel()
		}
	}()

	return srv
}

// redirectToHTTPS redirects the request to HTTPS using the configured SNI port.
func redirectToHTTPS(w http.ResponseWriter, r *http.Request, sniListenAddr string) {
	host := strings.TrimSpace(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	// Extract port from sniListenAddr (e.g., ":443", "443", "example.com:443")
	port := defaultHTTPSPort
	if raw := strings.TrimSpace(sniListenAddr); raw != "" {
		switch {
		case strings.HasPrefix(raw, ":"):
			port = strings.TrimPrefix(raw, ":")
		case strings.Count(raw, ":") == 0:
			port = raw
		default:
			if _, p, err := net.SplitHostPort(raw); err == nil {
				port = p
			}
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			port = defaultHTTPSPort
		}
	}

	if port != defaultHTTPSPort {
		host = net.JoinHostPort(host, port)
	}

	target := "https://" + host + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusPermanentRedirect)
}

func handleKeylessSign(w http.ResponseWriter, r *http.Request, signer *keyless.Signer) {
	if signer == nil {
		writeSignError(w, http.StatusNotFound, "keyless signer is disabled")
		return
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeSignError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		writeSignError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
		return
	}

	defer r.Body.Close()

	var req keyless.SignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeSignError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	resp, err := signer.Sign(r.Context(), &req)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, keyless.ErrSignerDisabled):
			status = http.StatusNotFound
		case errors.Is(err, keyless.ErrInvalidArgument):
			status = http.StatusBadRequest
		case errors.Is(err, keyless.ErrPermissionDenied):
			status = http.StatusForbidden
		}
		writeSignError(w, status, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Error().Err(err).Msg("[signer] failed to encode sign response")
		writeSignError(w, http.StatusInternalServerError, "failed to encode response")
	}
}
