package main

import (
	"bufio"
	"context"
	"embed"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/sdk"
)

//go:embed dist/*
var distFS embed.FS

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(addr string, serv *portal.RelayServer, admin *Admin, frontend *Frontend, noIndex bool, cancel context.CancelFunc) *http.Server {
	if addr == "" {
		addr = ":0"
	}

	// Create app UI mux
	appMux := http.NewServeMux()

	// Serve favicons (ico/png/svg) from dist/app
	frontend.ServeAsset(appMux, "/favicon.ico", "favicon.ico", "image/x-icon")
	frontend.ServeAsset(appMux, "/favicon.png", "favicon.png", "image/png")
	frontend.ServeAsset(appMux, "/favicon.svg", "favicon.svg", "image/svg+xml")

	if noIndex {
		appMux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("User-agent: *\nDisallow: /\n"))
		})
	}

	// Portal app assets (JS, CSS, etc.) - served from /app/
	appMux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/app/")
		frontend.ServeAppStatic(w, r, p, serv)
	})

	// Tunnel installer script and binaries
	appMux.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		serveTunnelScript(w, r)
	})
	appMux.HandleFunc("/tunnel/bin/", func(w http.ResponseWriter, r *http.Request) {
		serveTunnelBinary(w, r)
	})

	// SDK Registry API for lease registration
	registry := &SDKRegistry{}
	appMux.HandleFunc("/sdk/", func(w http.ResponseWriter, r *http.Request) {
		registry.HandleSDKRequest(w, r, serv)
	})

	// App UI index page - serve React frontend with SSR (delegates to serveAppStatic)
	appMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// serveAppStatic handles both "/" and 404 fallback with SSR
		p := strings.TrimPrefix(r.URL.Path, "/")
		frontend.ServeAppStatic(w, r, p, serv)
	})

	appMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{\"status\":\"ok\"}"))
	})

	// Admin API
	appMux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		admin.HandleAdminRequest(w, r, serv)
	})

	// Create the main handler
	appDomain := defaultAppPattern(flagPortalURL)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Compatibility endpoints for legacy webclient deployments.
		// Handle before host-based routing so stale service workers can recover.
		if r.URL.Path == "/service-worker.js" {
			frontend.ServeLegacyServiceWorkerCleanup(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/frontend/") {
			frontend.ServeLegacyFrontendCompat(w, r)
			return
		}

		// Handle subdomain requests
		if isSubdomain(appDomain, r.Host) {
			log.Debug().
				Str("host", r.Host).
				Str("url", r.URL.String()).
				Msg("[server] handling subdomain request")
			// Check if the tunnel has TLS enabled by looking up the lease
			if shouldProxyHTTP(r.Host, serv) {
				// TLS is not enabled on the tunnel, proxy via HTTP
				log.Debug().Str("host", r.Host).Msg("[server] proxying to HTTP")
				proxyToHTTP(w, r, serv)
				return
			}
			// TLS is enabled, redirect to HTTPS.
			log.Debug().Str("host", r.Host).Msg("[server] redirecting to HTTPS")
			redirectToHTTPS(w, r, serv.GetSNIRouter().GetAddr())
			return
		}
		appMux.ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	go func() {
		log.Info().Msgf("[server] http: %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("[server] http error")
			cancel()
		}
	}()

	return srv
}

// shouldProxyHTTP checks if the request should be proxied via HTTP.
// Returns true if TLS mode is no-tls.
func shouldProxyHTTP(host string, serv *portal.RelayServer) bool {
	leaseName, ok := leaseNameFromHost(host, defaultAppPattern(flagPortalURL))
	if !ok {
		log.Debug().Str("host", host).Msg("[proxy] shouldProxyHTTP: failed to extract lease name")
		return false
	}

	entry, ok := serv.GetLeaseManager().GetLeaseByName(leaseName)
	if !ok {
		log.Debug().Str("lease_name", leaseName).Msg("[proxy] shouldProxyHTTP: lease not found")
		return true
	}

	// If TLS mode is no-tls, we can proxy via HTTP.
	shouldProxy := normalizeTLSMode(sdk.TLSMode(entry.Lease.TLSMode)) == sdk.TLSModeNoTLS
	log.Debug().
		Str("lease_name", leaseName).
		Str("tls_mode", entry.Lease.TLSMode).
		Msg("[proxy] shouldProxyHTTP")
	return shouldProxy
}

func proxyToHTTP(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer) {
	leaseName, ok := leaseNameFromHost(r.Host, defaultAppPattern(flagPortalURL))
	if !ok {
		http.Error(w, "invalid subdomain", http.StatusBadRequest)
		return
	}

	entry, ok := serv.GetLeaseManager().GetLeaseByName(leaseName)
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	if normalizeTLSMode(sdk.TLSMode(entry.Lease.TLSMode)) != sdk.TLSModeNoTLS {
		http.Error(w, "TLS enabled requires HTTPS access", http.StatusBadRequest)
		return
	}

	reverseConn, err := serv.GetReverseHub().AcquireForHTTP(entry.Lease.ID, portal.HTTPProxyWait)
	if err != nil {
		log.Error().
			Err(err).
			Str("lease", leaseName).
			Str("lease_id", entry.Lease.ID).
			Msg("[proxy] failed to connect to backend")
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	defer reverseConn.Close()
	targetConn := reverseConn.Conn

	// Write the HTTP request to the tunnel
	if err := r.Write(targetConn); err != nil {
		log.Error().Err(err).Msg("[proxy] failed to write request to tunnel")
		http.Error(w, "proxy error", http.StatusInternalServerError)
		return
	}

	// Read the response from the tunnel
	resp, err := http.ReadResponse(bufio.NewReader(targetConn), r)
	if err != nil {
		log.Error().Err(err).Msg("[proxy] failed to read response from tunnel")
		http.Error(w, "proxy error", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// Copy headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}

	// Write status code
	w.WriteHeader(resp.StatusCode)

	// Copy body
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Debug().Err(err).Msg("[proxy] error copying response body")
	}
}

// redirectToHTTPS redirects the request to HTTPS using the configured SNI port.
func redirectToHTTPS(w http.ResponseWriter, r *http.Request, sniListenAddr string) {
	host := strings.TrimSpace(r.Host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	// Extract port from sniListenAddr (e.g., ":443", "443", "example.com:443")
	port := "443"
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
			port = "443"
		}
	}

	if port != "443" {
		host = net.JoinHostPort(host, port)
	}

	target := "https://" + host + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}
