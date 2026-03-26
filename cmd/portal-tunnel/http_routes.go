package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/utils"
)

type httpRoute struct {
	prefix   string
	upstream *url.URL
	proxy    *httputil.ReverseProxy
}

func newHTTPRouteHandler(rawRoutes []string) (http.Handler, error) {
	if len(rawRoutes) == 0 {
		return nil, errors.New("at least one --http-route is required")
	}

	routes := make([]*httpRoute, 0, len(rawRoutes))
	seen := make(map[string]struct{}, len(rawRoutes))
	for _, raw := range rawRoutes {
		route, err := parseHTTPRoute(raw)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[route.prefix]; ok {
			return nil, fmt.Errorf("duplicate --http-route prefix %q", route.prefix)
		}
		seen[route.prefix] = struct{}{}
		route.proxy = route.newReverseProxy()
		routes = append(routes, route)
	}

	// longest-prefix-first
	sort.Slice(routes, func(i, j int) bool {
		if len(routes[i].prefix) == len(routes[j].prefix) {
			return routes[i].prefix < routes[j].prefix
		}
		return len(routes[i].prefix) > len(routes[j].prefix)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "" {
			p = "/"
		}
		for _, route := range routes {
			if route.prefix == "/" || p == route.prefix || strings.HasPrefix(p, route.prefix+"/") {
				route.proxy.ServeHTTP(w, r)
				return
			}
		}
		http.NotFound(w, r)
	}), nil
}

func parseHTTPRoute(raw string) (*httpRoute, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("--http-route: expected PATH=UPSTREAM")
	}

	prefixRaw, upstreamRaw, ok := strings.Cut(raw, "=")
	if !ok {
		return nil, fmt.Errorf("--http-route %q: expected PATH=UPSTREAM", raw)
	}

	prefix := strings.TrimSpace(prefixRaw)
	if prefix == "" {
		return nil, fmt.Errorf("--http-route %q: prefix is required", raw)
	}
	if !strings.HasPrefix(prefix, "/") {
		return nil, fmt.Errorf("--http-route %q: prefix must start with /", raw)
	}
	prefix = utils.NormalizeURLPath(prefix)

	upstreamInput := strings.TrimSpace(upstreamRaw)
	if upstreamInput == "" {
		return nil, fmt.Errorf("--http-route %q: upstream is required", raw)
	}
	if !strings.Contains(upstreamInput, "://") {
		target, err := utils.NormalizeLoopbackTarget(upstreamInput)
		if err != nil {
			return nil, fmt.Errorf("--http-route %q: %w", raw, err)
		}
		upstreamInput = "http://" + target
	}

	upstream, err := url.Parse(upstreamInput)
	if err != nil {
		return nil, fmt.Errorf("--http-route %q: %w", raw, err)
	}
	if upstream.Host == "" {
		return nil, fmt.Errorf("--http-route %q: upstream host is required", raw)
	}
	if upstream.Scheme != "http" && upstream.Scheme != "https" {
		return nil, fmt.Errorf("--http-route %q: scheme must be http or https", raw)
	}
	upstream.Fragment = ""
	upstream.Path = utils.NormalizeURLPath(upstream.Path)

	return &httpRoute{prefix: prefix, upstream: upstream}, nil
}

func (r *httpRoute) newReverseProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: r.rewriteRequest,
		ModifyResponse: func(resp *http.Response) error {
			if resp == nil || resp.Request == nil {
				return nil
			}
			publicHost := resp.Request.Header.Get("X-Forwarded-Host")
			publicScheme := resp.Request.Header.Get("X-Forwarded-Proto")
			r.rewriteLocation(resp.Header, publicHost, publicScheme)
			r.rewriteSetCookies(resp.Header, publicHost)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			log.Error().Err(err).
				Str("route_prefix", r.prefix).
				Str("upstream", r.upstream.String()).
				Msg("http route proxy failed")
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}
}

func (r *httpRoute) rewriteRequest(pr *httputil.ProxyRequest) {
	// strip route prefix from the path before forwarding
	reqPath := utils.NormalizeURLPath(pr.In.URL.Path)
	rawPath := pr.In.URL.RawPath
	if r.prefix != "/" {
		if reqPath == r.prefix {
			reqPath = "/"
			rawPath = ""
		} else {
			reqPath = strings.TrimPrefix(reqPath, r.prefix)
			if reqPath == "" {
				reqPath = "/"
			}
			if rawPath == r.prefix {
				rawPath = "/"
			} else if strings.HasPrefix(rawPath, r.prefix+"/") {
				rawPath = strings.TrimPrefix(rawPath, r.prefix)
			}
		}
	}

	pr.Out.URL.Path = reqPath
	pr.Out.URL.RawPath = rawPath
	pr.Out.URL.RawQuery = pr.In.URL.RawQuery
	pr.SetURL(r.upstream)
	pr.SetXForwarded()

	// SetXForwarded checks pr.In.TLS, but behind a TLS-terminating proxy
	// the inbound X-Forwarded-Proto carries the real client scheme.
	if pr.In.TLS == nil {
		proto, _, _ := strings.Cut(pr.In.Header.Get("X-Forwarded-Proto"), ",")
		if proto = strings.ToLower(strings.TrimSpace(proto)); proto != "" {
			pr.Out.Header.Set("X-Forwarded-Proto", proto)
		}
	}

	if r.prefix != "/" {
		pr.Out.Header.Set("X-Forwarded-Prefix", r.prefix)
	}
}

func (r *httpRoute) rewriteLocation(header http.Header, publicHost, publicScheme string) {
	location := header.Get("Location")
	if location == "" {
		return
	}
	parsed, err := url.Parse(location)
	if err != nil {
		return
	}

	switch {
	case parsed.IsAbs():
		if !strings.EqualFold(parsed.Scheme, r.upstream.Scheme) || !strings.EqualFold(parsed.Host, r.upstream.Host) {
			return
		}
		parsed.Scheme = publicScheme
		parsed.Host = publicHost
	case strings.HasPrefix(location, "/") && parsed.Host == "" && (len(location) == 1 || (location[1] != '\\' && location[1] != '/')):
		// server-relative redirect
	default:
		return
	}

	mapped := r.mapUpstreamPathToPublic(parsed.Path)
	if !strings.HasPrefix(mapped, "/") || (len(mapped) > 1 && (mapped[1] == '/' || mapped[1] == '\\')) {
		return
	}
	parsed.Path = mapped
	parsed.RawPath = ""
	header.Set("Location", parsed.String())
}

func (r *httpRoute) rewriteSetCookies(header http.Header, publicHost string) {
	values := header.Values("Set-Cookie")
	if len(values) == 0 {
		return
	}

	publicDomain := publicHost
	if host, port, err := net.SplitHostPort(publicDomain); err == nil && port != "" {
		publicDomain = host
	}
	publicDomain = strings.ToLower(strings.Trim(publicDomain, "[]"))
	upstreamDomain := strings.ToLower(r.upstream.Hostname())

	header.Del("Set-Cookie")
	for _, value := range values {
		cookie, err := http.ParseSetCookie(value)
		if err != nil {
			header.Add("Set-Cookie", value)
			continue
		}

		changed := false
		if cookie.Path != "" {
			if rewritten := r.mapUpstreamPathToPublic(cookie.Path); rewritten != cookie.Path {
				cookie.Path = rewritten
				changed = true
			}
		}

		domain := strings.ToLower(strings.TrimPrefix(cookie.Domain, "."))
		if domain != "" && domain != publicDomain &&
			(domain == upstreamDomain || utils.IsLocalRelayHost(domain)) {
			cookie.Domain = ""
			changed = true
		}

		if changed {
			header.Add("Set-Cookie", cookie.String())
		} else {
			header.Add("Set-Cookie", value)
		}
	}
}

func (r *httpRoute) mapUpstreamPathToPublic(raw string) string {
	raw = utils.NormalizeURLPath(raw)
	if r.prefix != "/" && (raw == r.prefix || strings.HasPrefix(raw, r.prefix+"/")) {
		return raw
	}

	base := utils.NormalizeURLPath(r.upstream.Path)
	rest := raw
	if base != "/" {
		if raw == base {
			rest = "/"
		} else if strings.HasPrefix(raw, base+"/") {
			rest = strings.TrimPrefix(raw, base)
		}
	}

	if r.prefix == "/" {
		return rest
	}
	if rest == "/" {
		return r.prefix
	}
	return r.prefix + rest
}
