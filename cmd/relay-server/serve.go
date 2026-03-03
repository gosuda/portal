package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/keyless"
)

//go:embed dist/*
var distFS embed.FS

// serveAPI builds the admin/API mux and returns the server.
func serveAPI(addr string, serv *portal.RelayServer, admin *Admin, frontend *Frontend, cancel context.CancelFunc) *http.Server {
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

	// Keyless signer endpoint.
	appMux.HandleFunc("/v1/sign", func(w http.ResponseWriter, r *http.Request) {
		handleKeylessSign(w, r, serv.GetKeylessSigner())
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
			leaseName, leaseEntry, shouldProxy := shouldProxyHTTP(r.Host, serv)
			if shouldProxy {
				// TLS is not enabled on the tunnel, proxy via HTTP
				log.Debug().Str("host", r.Host).Msg("[server] proxying to HTTP")
				proxyToHTTP(w, r, serv, leaseName, leaseEntry)
				return
			}
			// TLS-enabled subdomains should terminate on SNI passthrough.
			// Redirect only insecure requests; secure requests here would loop.
			if !isSecureRequest(r) {
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
		Addr:         addr,
		Handler:      handler,
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}
	tlsCertFile, tlsKeyFile := "", ""
	if acmeManager := serv.GetACMEManager(); acmeManager != nil {
		tlsCertFile, tlsKeyFile = acmeManager.TLSFiles()
	}

	go func() {
		var err error
		if tlsCertFile != "" && tlsKeyFile != "" {
			log.Info().Str("addr", addr).Str("cert_file", tlsCertFile).Str("key_file", tlsKeyFile).Msg("[server] https api enabled")
			err = srv.ListenAndServeTLS(tlsCertFile, tlsKeyFile)
		} else {
			log.Info().Str("addr", addr).Msgf("[server] http api enabled")
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("[server] http error")
			cancel()
		}
	}()

	return srv
}

// shouldProxyHTTP checks if the request should be proxied via HTTP.
// It returns leaseName, lease entry, and whether HTTP proxying should be used.
func shouldProxyHTTP(host string, serv *portal.RelayServer) (string, *portal.LeaseEntry, bool) {
	leaseName, ok := leaseNameFromHost(host, defaultAppPattern(flagPortalURL))
	if !ok {
		log.Debug().Str("host", host).Msg("[proxy] shouldProxyHTTP: failed to extract lease name")
		return "", nil, false
	}

	entry, ok := serv.GetLeaseManager().GetLeaseByName(leaseName)
	if !ok {
		log.Debug().Str("lease_name", leaseName).Msg("[proxy] shouldProxyHTTP: lease not found")
		return leaseName, nil, true
	}

	// If TLS is disabled, we can proxy via HTTP.
	shouldProxy := !entry.Lease.TLS
	log.Debug().
		Str("lease_name", leaseName).
		Bool("tls", entry.Lease.TLS).
		Msg("[proxy] shouldProxyHTTP")
	return leaseName, entry, shouldProxy
}

func proxyToHTTP(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer, leaseName string, entry *portal.LeaseEntry) {
	if leaseName == "" {
		http.Error(w, "invalid subdomain", http.StatusBadRequest)
		return
	}

	if entry == nil {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	if entry.Lease.TLS {
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
