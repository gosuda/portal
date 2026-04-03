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
	prefix            string
	prefixSlash       string
	upstream          *url.URL
	upstreamPath      string
	upstreamPathSlash string
	upstreamDomain    string
	proxy             *httputil.ReverseProxy
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
			if route.prefix == "/" || p == route.prefix || strings.HasPrefix(p, route.prefixSlash) {
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

	route := &httpRoute{
		prefix:         prefix,
		upstream:       upstream,
		upstreamPath:   upstream.Path,
		upstreamDomain: utils.NormalizeHostname(upstream.Hostname()),
	}
	if prefix != "/" {
		route.prefixSlash = prefix + "/"
	}
	if upstream.Path != "/" {
		route.upstreamPathSlash = upstream.Path + "/"
	}
	return route, nil
}

func (r *httpRoute) newReverseProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite:        r.rewriteRequest,
		ModifyResponse: r.rewriteResponse,
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
	pr.Out.URL.Path, pr.Out.URL.RawPath = r.publicRequestPathToUpstream(pr.In.URL.Path, pr.In.URL.RawPath)
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

func (r *httpRoute) rewriteResponse(resp *http.Response) error {
	if resp == nil || resp.Request == nil {
		return nil
	}

	header := resp.Header
	publicHost := resp.Request.Header.Get("X-Forwarded-Host")
	publicScheme := resp.Request.Header.Get("X-Forwarded-Proto")

	location := header.Get("Location")
	if location != "" {
		parsed, err := url.Parse(location)
		if err == nil {
			switch {
			case parsed.IsAbs():
				if strings.EqualFold(parsed.Scheme, r.upstream.Scheme) && strings.EqualFold(parsed.Host, r.upstream.Host) {
					parsed.Scheme = publicScheme
					parsed.Host = publicHost
				} else {
					parsed = nil
				}
			case strings.HasPrefix(location, "/") && parsed.Host == "" && (len(location) == 1 || (location[1] != '\\' && location[1] != '/')):
				// server-relative redirect
			default:
				parsed = nil
			}

			if parsed != nil {
				mapped := r.upstreamPathToPublic(parsed.Path)
				if strings.HasPrefix(mapped, "/") && (len(mapped) == 1 || (mapped[1] != '/' && mapped[1] != '\\')) {
					parsed.Path = mapped
					parsed.RawPath = ""
					header.Set("Location", parsed.String())
				}
			}
		}
	}

	values := header.Values("Set-Cookie")
	if len(values) == 0 {
		return nil
	}

	publicDomain := publicHost
	if host, port, err := net.SplitHostPort(publicDomain); err == nil && port != "" {
		publicDomain = host
	}
	publicDomain = utils.NormalizeHostname(strings.Trim(publicDomain, "[]"))

	header.Del("Set-Cookie")
	for _, value := range values {
		cookie, err := http.ParseSetCookie(value)
		if err != nil {
			header.Add("Set-Cookie", value)
			continue
		}

		changed := false
		if cookie.Path != "" {
			if rewritten := r.upstreamPathToPublic(cookie.Path); rewritten != cookie.Path {
				cookie.Path = rewritten
				changed = true
			}
		}

		domain := utils.NormalizeHostname(strings.TrimPrefix(cookie.Domain, "."))
		if domain != "" && domain != publicDomain &&
			(domain == r.upstreamDomain || utils.IsLocalRelayHost(domain)) {
			cookie.Domain = ""
			changed = true
		}

		if changed {
			header.Add("Set-Cookie", cookie.String())
			continue
		}
		header.Add("Set-Cookie", value)
	}

	return nil
}

func (r *httpRoute) publicRequestPathToUpstream(path, rawPath string) (string, string) {
	path = utils.NormalizeURLPath(path)
	if r.prefix == "/" {
		return path, rawPath
	}
	if path == r.prefix {
		return "/", ""
	}
	path = strings.TrimPrefix(path, r.prefix)
	if path == "" {
		path = "/"
	}

	if rawPath != "" {
		switch {
		case rawPath == r.prefix:
			rawPath = "/"
		case strings.HasPrefix(rawPath, r.prefixSlash):
			rawPath = strings.TrimPrefix(rawPath, r.prefix)
		}
	}
	return path, rawPath
}

func (r *httpRoute) upstreamPathToPublic(raw string) string {
	raw = utils.NormalizeURLPath(raw)
	if r.prefix != "/" && (raw == r.prefix || strings.HasPrefix(raw, r.prefixSlash)) {
		return raw
	}

	rest := raw
	if r.upstreamPath != "/" {
		if raw == r.upstreamPath {
			rest = "/"
		} else if strings.HasPrefix(raw, r.upstreamPathSlash) {
			rest = strings.TrimPrefix(raw, r.upstreamPath)
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
