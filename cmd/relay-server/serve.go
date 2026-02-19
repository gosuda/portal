package main

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

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
func serveHTTP(addr string, serv *portal.RelayServer, admin *Admin, frontend *Frontend, noIndex bool, certHash []byte, portalAppURL, portalURL string, shutdown context.CancelFunc) *http.Server {
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
		appMux.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte("User-agent: *\nDisallow: /\n")); err != nil {
				log.Error().Err(err).Msg("[server] write robots.txt response")
			}
		})
	}

	// Portal app assets (JS, CSS, etc.) - served from /app/
	appMux.HandleFunc("/app/", withCORSMiddleware(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/app/")
		frontend.ServeAppStatic(w, r, p, serv)
	}))

	// Portal frontend files (for unified caching).
	appMux.HandleFunc("/frontend/", withCORSMiddleware(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/frontend/")
		frontend.ServePortalStaticFile(w, r, p)
	}))

	// Tunnel installer script and binaries
	appMux.HandleFunc("/tunnel", func(w http.ResponseWriter, r *http.Request) {
		serveTunnelScript(w, r, portalURL)
	})
	appMux.HandleFunc("/tunnel/bin/", func(w http.ResponseWriter, r *http.Request) {
		serveTunnelBinary(w, r)
	})

	// The /relay endpoint is served via HTTP/3 WebTransport (see serveWebTransport).
	// Return 426 Upgrade Required for HTTP/1.1 clients hitting this path.
	appMux.HandleFunc("/relay", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		http.Error(w, "WebTransport (HTTP/3) required", http.StatusUpgradeRequired)
	})

	// App UI index page - serve React frontend with SSR (delegates to serveAppStatic)
	appMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// serveAppStatic handles both "/" and 404 fallback with SSR
		p := strings.TrimPrefix(r.URL.Path, "/")
		frontend.ServeAppStatic(w, r, p, serv)
	})

	appMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("{\"status\":\"ok\"}")); err != nil {
			log.Error().Err(err).Msg("[server] write healthz response")
		}
	})

	if len(certHash) > 0 {
		hashHex := hex.EncodeToString(certHash)
		appMux.HandleFunc("/cert-hash", func(w http.ResponseWriter, _ *http.Request) {
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

	// Static file handler for /frontend/ (for unified caching).
	portalMux.HandleFunc("/frontend/", withCORSMiddleware(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/frontend/")
		frontend.ServePortalStaticFile(w, r, p)
	}))

	// Root and SPA fallback for portal subdomains.
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
		if utils.IsSubdomain(portalAppURL, r.Host) {
			portalMux.ServeHTTP(w, r)
		} else {
			appMux.ServeHTTP(w, r)
		}
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info().Msgf("[server] http: %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("[server] http error")
			shutdown()
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
func serveWebTransport(addr string, serv *portal.RelayServer, admin *Admin, tlsCert *tls.Certificate, shutdown context.CancelFunc) func() {
	mux := http.NewServeMux()

	wtServer := &webtransport.Server{
		H3: &http3.Server{
			Addr: addr,
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{*tlsCert},
				MinVersion:   tls.VersionTLS13,
			},
			Handler: mux,
		},
	}

	mux.HandleFunc("/relay", func(w http.ResponseWriter, r *http.Request) {
		handleWebTransportRelayRequest(w, r, admin, wtServer.Upgrade, serv.HandleSession)
	})

	go func() {
		log.Info().Msgf("[server] http/3 (webtransport): %s", addr)
		if err := wtServer.ListenAndServe(); err != nil {
			log.Error().Err(err).Msg("[server] http/3 error")
			shutdown()
		}
	}()

	return func() {
		if err := wtServer.Close(); err != nil {
			log.Error().Err(err).Msg("[server] webtransport shutdown error")
		}
	}
}

func handleWebTransportRelayRequest(
	w http.ResponseWriter,
	r *http.Request,
	admin *Admin,
	upgrade func(http.ResponseWriter, *http.Request) (*webtransport.Session, error),
	handleSession func(portal.Session),
) {
	clientIP := manager.ExtractClientIP(r)
	if ipManager := admin.GetIPManager(); ipManager != nil && ipManager.IsIPBanned(clientIP) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	sess, err := upgrade(w, r)
	if err != nil {
		log.Error().Err(err).Msg("[server] webtransport upgrade failed")
		return
	}

	handleSession(wrapRelaySession(portal.NewWTSession(sess), clientIP))
}

type relaySession struct {
	portal.Session
	clientIP string
}

func wrapRelaySession(session portal.Session, clientIP string) portal.Session {
	if clientIP == "" {
		return session
	}
	return &relaySession{
		Session:  session,
		clientIP: clientIP,
	}
}

func (s *relaySession) OpenStream(ctx context.Context) (portal.Stream, error) {
	stream, err := s.Session.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	return wrapRelayStream(stream, s.clientIP), nil
}

func (s *relaySession) AcceptStream(ctx context.Context) (portal.Stream, error) {
	stream, err := s.Session.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return wrapRelayStream(stream, s.clientIP), nil
}

type relayStream struct {
	portal.Stream
	clientIP string
}

func wrapRelayStream(stream portal.Stream, clientIP string) portal.Stream {
	if clientIP == "" || stream == nil {
		return stream
	}
	return &relayStream{
		Stream:   stream,
		clientIP: clientIP,
	}
}

func streamClientIP(stream portal.Stream) string {
	relayStream, ok := stream.(*relayStream)
	if !ok {
		return ""
	}
	return relayStream.clientIP
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
