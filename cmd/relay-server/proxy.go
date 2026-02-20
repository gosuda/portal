package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/idna"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/utils"
)

type contextKey string

const leaseIDContextKey contextKey = "leaseID"

// HTTPProxy is a server-side HTTP reverse proxy that tunnels requests
// to backend apps connected via portal tunnel. This makes all traffic
// same-origin, enabling native Set-Cookie header support.
type HTTPProxy struct {
	server       *portal.RelayServer
	reverseProxy *httputil.ReverseProxy
}

// NewHTTPProxy creates a new HTTP reverse proxy for subdomain tunneling.
func NewHTTPProxy(server *portal.RelayServer) *HTTPProxy {
	p := &HTTPProxy{server: server}

	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// addr is "leaseID:80" from the rewritten URL
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				host = addr
			}
			return server.DialLease(host, "http/1.1")
		},
	}

	p.reverseProxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			leaseID := pr.In.Context().Value(leaseIDContextKey).(string)
			pr.SetURL(&url.URL{
				Scheme: "http",
				Host:   leaseID,
			})
			pr.SetXForwarded()
		},
		Transport:     transport,
		FlushInterval: -1, // stream responses immediately
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Error().Err(err).
				Str("path", r.URL.Path).
				Str("host", r.Host).
				Msg("[HTTPProxy] reverse proxy error")
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}

	return p
}

// extractLeaseName extracts the lease name from the subdomain of the Host header.
// Example: "demo-app.portal.example.com:4017" -> "demo-app"
// Handles punycode/IDN domains.
func extractLeaseName(host string) string {
	h := strings.ToLower(utils.StripPort(utils.StripScheme(host)))
	p := strings.ToLower(utils.StripPort(utils.StripScheme(flagPortalAppURL)))

	if strings.HasPrefix(p, "*.") {
		suffix := p[1:] // ".example.com"
		if len(h) > len(suffix) && strings.HasSuffix(h, suffix) {
			name := h[:len(h)-len(suffix)]
			// Handle URL-encoded characters
			if decoded, err := url.QueryUnescape(name); err == nil {
				name = decoded
			}
			// Handle punycode/IDN
			if unicode, err := idna.ToUnicode(name); err == nil {
				name = unicode
			}
			return name
		}
	}

	// Handle non-wildcard patterns (e.g., "sub.example.com" as base)
	if len(h) > len(p)+1 && strings.HasSuffix(h, "."+p) {
		name := h[:len(h)-len(p)-1]
		if decoded, err := url.QueryUnescape(name); err == nil {
			name = decoded
		}
		if unicode, err := idna.ToUnicode(name); err == nil {
			name = unicode
		}
		return name
	}

	return ""
}

// resolveLease resolves a lease name to a lease ID using case-insensitive matching.
func (p *HTTPProxy) resolveLease(name string) (string, bool) {
	entry, ok := p.server.GetLeaseByNameFold(name)
	if !ok {
		return "", false
	}
	return entry.Lease.Identity.Id, true
}

// isWebSocketUpgrade checks if the request is a WebSocket upgrade request.
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// TryProxy attempts to reverse-proxy the request to a tunnel backend.
// Returns true if the request was handled (proxied or WebSocket), false if
// no matching lease was found (caller should fall back to portal HTML).
func (p *HTTPProxy) TryProxy(w http.ResponseWriter, r *http.Request) bool {
	leaseName := extractLeaseName(r.Host)
	if leaseName == "" {
		return false
	}

	leaseID, ok := p.resolveLease(leaseName)
	if !ok {
		return false
	}

	if isWebSocketUpgrade(r) {
		p.handleWebSocket(w, r, leaseID)
		return true
	}

	// HTTP reverse proxy with lease ID in context
	ctx := context.WithValue(r.Context(), leaseIDContextKey, leaseID)
	p.reverseProxy.ServeHTTP(w, r.WithContext(ctx))
	return true
}

// handleWebSocket proxies a WebSocket upgrade request through the tunnel.
func (p *HTTPProxy) handleWebSocket(w http.ResponseWriter, r *http.Request, leaseID string) {
	// 1. Dial backend through tunnel
	backendConn, err := p.server.DialLease(leaseID, "http/1.1")
	if err != nil {
		log.Error().Err(err).Str("lease_id", leaseID).Msg("[HTTPProxy] WebSocket: failed to dial lease")
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	// 2. Hijack client's TCP connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		log.Error().Msg("[HTTPProxy] WebSocket: response writer does not support hijacking")
		backendConn.Close()
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		log.Error().Err(err).Msg("[HTTPProxy] WebSocket: failed to hijack connection")
		backendConn.Close()
		return
	}

	// 3. Write the original upgrade request to backend
	if err := r.Write(backendConn); err != nil {
		log.Error().Err(err).Msg("[HTTPProxy] WebSocket: failed to write upgrade request to backend")
		clientConn.Close()
		backendConn.Close()
		return
	}

	// 4. Read backend response and forward to client
	backendBuf := bufio.NewReader(backendConn)
	resp, err := http.ReadResponse(backendBuf, r)
	if err != nil {
		log.Error().Err(err).Msg("[HTTPProxy] WebSocket: failed to read backend response")
		clientConn.Close()
		backendConn.Close()
		return
	}

	if err := resp.Write(clientConn); err != nil {
		log.Error().Err(err).Msg("[HTTPProxy] WebSocket: failed to write response to client")
		clientConn.Close()
		backendConn.Close()
		return
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		clientConn.Close()
		backendConn.Close()
		return
	}

	// 5. Bidirectional relay
	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(backendConn, clientConn)
		errc <- err
	}()
	go func() {
		// Use backendBuf to drain any data buffered during ReadResponse
		_, err := io.Copy(clientConn, backendBuf)
		errc <- err
	}()

	<-errc
	clientConn.Close()
	backendConn.Close()
}
