package main

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
	"github.com/rs/zerolog/log"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/utils"
)

//go:embed dist/*
var distFS embed.FS

// serveHTTP builds the HTTP mux and returns the server.
func serveHTTP(addr string, serv *portal.RelayServer, admin *Admin, frontend *Frontend, noIndex bool, certHash []byte, cancel context.CancelFunc) *http.Server {
	if addr == "" {
		addr = ":0"
	}

	// Initialize WASM cache used by content handlers
	if err := frontend.InitWasmCache(); err != nil {
		log.Error().Err(err).Msg("failed to initialize WASM cache")
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

	// Portal frontend files (for unified caching)
	appMux.HandleFunc("/frontend/", withCORSMiddleware(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/frontend/")
		if p == "manifest.json" {
			frontend.ServeDynamicManifest(w, r)
			return
		}

		frontend.ServePortalStaticFile(w, r, p)
	}))

	// Tunnel installer script and binaries
	appMux.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		serveTunnelScript(w, r)
	})
	appMux.HandleFunc("/tunnel/bin/", func(w http.ResponseWriter, r *http.Request) {
		serveTunnelBinary(w, r)
	})

	appMux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Check if IP is banned
		clientIP := manager.ExtractClientIP(r)
		ipManager := admin.GetIPManager()
		if ipManager != nil && ipManager.IsIPBanned(clientIP) {
			log.Warn().Str("ip", clientIP).Msg("[server] connection rejected: IP banned")
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		stream, wsConn, err := utils.UpgradeToWSStream(w, r, nil)
		if err != nil {
			log.Error().Err(err).Msg("[server] websocket upgrade failed")
			return
		}

		// Store pending IP for lease association (will be linked when lease is registered)
		if ipManager != nil && clientIP != "" {
			ipManager.StorePendingIP(clientIP)
		}

		sess, err := portal.NewYamuxServerSession(stream)
		if err != nil {
			log.Error().Err(err).Msg("[server] failed to create yamux session")
			wsConn.Close()
			return
		}
		serv.HandleSession(sess)
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

	if len(certHash) > 0 {
		hashHex := hex.EncodeToString(certHash)
		appMux.HandleFunc("/cert-hash", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, `{"algorithm":"sha-256","hash":"%s"}`, hashHex)
		})
	}

	// Admin API
	appMux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		admin.HandleAdminRequest(w, r, serv)
	})

	// Create portal frontend mux (routes only)
	portalMux := http.NewServeMux()

	// Static file handler for /frontend/ (for unified caching)
	portalMux.HandleFunc("/frontend/", withCORSMiddleware(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/frontend/")
		if p == "manifest.json" {
			frontend.ServeDynamicManifest(w, r)
			return
		}
		frontend.ServePortalStaticFile(w, r, p)
	}))

	// Service worker for portal subdomains (serve from dist/wasm)
	portalMux.HandleFunc("/service-worker.js", func(w http.ResponseWriter, r *http.Request) {
		frontend.ServeDynamicServiceWorker(w, r)
	})

	// Root and SPA fallback for portal subdomains
	portalMux.HandleFunc("/", withCORSMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			// Serve portal HTML with SSR for OG metadata
			frontend.ServePortalHTMLWithSSR(w, r, serv)
			return
		}
		frontend.ServePortalStatic(w, r)
	}))

	// routes based on host and path
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Route subdomain requests (e.g., *.example.com) to portalMux
		// and everything else to the app UI mux.
		if utils.IsSubdomain(flagPortalAppURL, r.Host) {
			portalMux.ServeHTTP(w, r)
		} else {
			appMux.ServeHTTP(w, r)
		}
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

// serveWebTransport starts the HTTP/3 WebTransport server.
func serveWebTransport(addr string, serv *portal.RelayServer, tlsCert *tls.Certificate, cancel context.CancelFunc) func() {
	mux := http.NewServeMux()

	wtServer := &webtransport.Server{
		H3: &http3.Server{
			Addr: addr,
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{*tlsCert},
			},
			Handler: mux,
		},
	}

	mux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		sess, err := wtServer.Upgrade(w, r)
		if err != nil {
			log.Error().Err(err).Msg("[server] webtransport upgrade failed")
			return
		}
		serv.HandleSession(portal.NewWTSession(sess))
	})

	go func() {
		log.Info().Msgf("[server] http/3 (webtransport): %s", addr)
		if err := wtServer.ListenAndServe(); err != nil {
			log.Error().Err(err).Msg("[server] http/3 error")
			cancel()
		}
	}()

	return func() {
		wtServer.Close()
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
