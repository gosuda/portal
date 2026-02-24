package main

import (
	"bufio"
	"context"
	"embed"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/websocket"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/utils/sni"
	"gosuda.org/portal/utils"
)

//go:embed dist/*
var distFS embed.FS

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(addr, sniListenAddr string, serv *portal.RelayServer, sniRouter *sni.Router, admin *Admin, frontend *Frontend, noIndex bool, cancel context.CancelFunc) *http.Server {
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
	appMux.HandleFunc("/app/", withCORSMiddleware(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/app/")
		frontend.ServeAppStatic(w, r, p, serv)
	}))

	// Tunnel installer script and binaries
	appMux.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		serveTunnelScript(w, r)
	})
	appMux.HandleFunc("/tunnel/bin/", func(w http.ResponseWriter, r *http.Request) {
		serveTunnelBinary(w, r)
	})

	// SDK Registry API for lease registration (used by SDK and tunnel clients)
	registry := NewSDKRegistry(serv, sniRouter)
	appMux.HandleFunc("/api/register", registry.HandleRegister)
	appMux.HandleFunc("/api/unregister", registry.HandleUnregister)
	appMux.HandleFunc("/api/renew", registry.HandleRenew)
	appMux.Handle("/api/connect", websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler:   websocket.Handler(serv.GetReverseHub().HandleConnect),
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
	appDomain := utils.DefaultAppPattern(flagPortalURL)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handle subdomain requests
		if utils.IsSubdomain(appDomain, r.Host) {
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
			redirectToHTTPS(w, r, sniListenAddr)
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

// leaseNameFromHost extracts the lease name from a subdomain host.
// It returns the lease name and true if the host is a valid subdomain of appURL.
func leaseNameFromHost(host, appURL string) (string, bool) {
	if !utils.IsSubdomain(appURL, host) {
		return "", false
	}

	normalizedHost := strings.ToLower(strings.TrimSpace(utils.StripPort(host)))
	baseHost := strings.ToLower(strings.TrimSpace(
		utils.StripPort(utils.StripWildCard(utils.StripScheme(appURL))),
	))

	if normalizedHost == "" || baseHost == "" || normalizedHost == baseHost {
		return "", false
	}

	suffix := "." + baseHost
	if !strings.HasSuffix(normalizedHost, suffix) {
		return "", false
	}

	leaseName := strings.TrimSuffix(normalizedHost, suffix)
	if leaseName == "" || strings.Contains(leaseName, ".") {
		// Lease names do not include dots; avoid ambiguous nested subdomains.
		return "", false
	}

	return leaseName, true
}

// redirectToHTTPS redirects the request to HTTPS using configured SNI port.
func redirectToHTTPS(w http.ResponseWriter, r *http.Request, sniListenAddr string) {
	targetHost := hostForHTTPSRedirect(r.Host, sniListenAddr)
	target := "https://" + targetHost + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	log.Debug().
		Str("from", r.URL.String()).
		Str("to", target).
		Msg("[server] redirecting to HTTPS")
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

func hostForHTTPSRedirect(requestHost, sniListenAddr string) string {
	host := strings.TrimSpace(requestHost)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}

	port := tlsPortForRedirect(sniListenAddr)
	if port == "443" {
		return host
	}

	return net.JoinHostPort(host, port)
}

func tlsPortForRedirect(sniListenAddr string) string {
	raw := strings.TrimSpace(sniListenAddr)
	if raw == "" {
		return "443"
	}

	port := ""
	switch {
	case strings.HasPrefix(raw, ":"):
		port = strings.TrimPrefix(raw, ":")
	case strings.Count(raw, ":") == 0:
		port = raw
	default:
		_, parsedPort, err := net.SplitHostPort(raw)
		if err != nil {
			return "443"
		}
		port = parsedPort
	}

	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "443"
	}
	return port
}

// shouldProxyHTTP checks if the request should be proxied via HTTP
// based on the lease's TLSEnabled setting.
// Returns true if TLS is NOT enabled (can proxy via HTTP).
func shouldProxyHTTP(host string, serv *portal.RelayServer) bool {
	leaseName, ok := leaseNameFromHost(host, utils.DefaultAppPattern(flagPortalURL))
	if !ok {
		log.Debug().Str("host", host).Msg("[proxy] shouldProxyHTTP: failed to extract lease name")
		return false
	}

	entry, ok := serv.GetLeaseManager().GetLeaseByName(leaseName)
	if !ok {
		log.Debug().Str("lease_name", leaseName).Msg("[proxy] shouldProxyHTTP: lease not found")
		return true
	}

	// If TLS is NOT enabled, we can proxy via HTTP
	shouldProxy := !entry.Lease.TLSEnabled
	log.Debug().
		Str("lease_name", leaseName).
		Bool("tls_enabled", entry.Lease.TLSEnabled).
		Bool("should_proxy_http", shouldProxy).
		Msg("[proxy] shouldProxyHTTP check")
	return shouldProxy
}

func proxyToHTTP(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer) {
	leaseName, ok := leaseNameFromHost(r.Host, utils.DefaultAppPattern(flagPortalURL))
	if !ok {
		http.Error(w, "invalid subdomain", http.StatusBadRequest)
		return
	}

	// Find lease by name
	entry, ok := serv.GetLeaseManager().GetLeaseByName(leaseName)
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	if entry.Lease.TLSEnabled {
		http.Error(w, "TLS enabled requires HTTPS access", http.StatusBadRequest)
		return
	}

	targetConn, releaseConn, err := openLeaseConnection(entry.Lease.ID, serv)
	if err != nil {
		log.Error().
			Err(err).
			Str("lease", leaseName).
			Str("target", entry.Lease.Address).
			Msg("[proxy] failed to connect to backend")
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	defer releaseConn()

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

func openLeaseConnection(leaseID string, serv *portal.RelayServer) (net.Conn, func(), error) {
	reverseConn, err := serv.GetReverseHub().AcquireStarted(leaseID, portal.ReverseHTTPWait)
	if err != nil {
		return nil, nil, fmt.Errorf("no reverse connection available for lease %s: %w", leaseID, err)
	}
	return reverseConn.Conn, reverseConn.Close, nil
}

func withCORSMiddleware(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		utils.SetCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		h(w, r)
	}
}

type leaseRow struct {
	Peer         string
	Name         string
	Kind         string
	Connected    bool
	DNS          string
	LastSeen     string
	LastSeenISO  string
	FirstSeenISO string
	TTL          string
	Link         string
	StaleRed     bool
	Hide         bool
	Metadata     string
	BPS          int64  // bytes-per-second limit (0 = unlimited)
	IsApproved   bool   // whether lease is approved (for manual mode)
	IsDenied     bool   // whether lease is denied (for manual mode)
	IP           string // client IP address (for IP-based ban)
	IsIPBanned   bool   // whether the IP is banned
}
