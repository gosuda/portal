package main

import (
	"context"
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

type httpRouteMeta struct {
	publicHost   string
	publicScheme string
}

type httpRouteMetaKey struct{}

func newHTTPRouteHandler(rawRoutes []string) (http.Handler, error) {
	if len(rawRoutes) == 0 {
		return nil, errors.New("at least one --http-route is required")
	}

	routes := make([]*httpRoute, 0, len(rawRoutes))
	seen := make(map[string]struct{}, len(rawRoutes))
	for _, rawRoute := range rawRoutes {
		route, err := parseHTTPRoute(rawRoute)
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

	sort.Slice(routes, func(i, j int) bool {
		if len(routes[i].prefix) == len(routes[j].prefix) {
			return routes[i].prefix < routes[j].prefix
		}
		return len(routes[i].prefix) > len(routes[j].prefix)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath := r.URL.Path
		if requestPath == "" {
			requestPath = "/"
		}
		for _, route := range routes {
			if route.prefix == "/" || requestPath == route.prefix || strings.HasPrefix(requestPath, route.prefix+"/") {
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
		return nil, errors.New("invalid --http-route: expected PATH=UPSTREAM")
	}

	prefixRaw, upstreamRaw, ok := strings.Cut(raw, "=")
	if !ok {
		return nil, fmt.Errorf("invalid --http-route %q: expected PATH=UPSTREAM", raw)
	}

	prefix := strings.TrimSpace(prefixRaw)
	switch {
	case prefix == "":
		return nil, fmt.Errorf("invalid --http-route prefix %q: %w", strings.TrimSpace(prefixRaw), errors.New("prefix is required"))
	case !strings.HasPrefix(prefix, "/"):
		return nil, fmt.Errorf("invalid --http-route prefix %q: %w", strings.TrimSpace(prefixRaw), errors.New("prefix must start with /"))
	}
	prefix = utils.NormalizeURLPath(prefix)

	upstreamInput := strings.TrimSpace(upstreamRaw)
	if upstreamInput == "" {
		return nil, fmt.Errorf("invalid --http-route upstream %q: %w", strings.TrimSpace(upstreamRaw), errors.New("upstream is required"))
	}

	if !strings.Contains(upstreamInput, "://") {
		target, err := utils.NormalizeLoopbackTarget(upstreamInput)
		if err != nil {
			return nil, fmt.Errorf("invalid --http-route upstream %q: %w", strings.TrimSpace(upstreamRaw), err)
		}
		upstreamInput = "http://" + target
	}

	upstream, err := url.Parse(upstreamInput)
	if err != nil {
		return nil, fmt.Errorf("invalid --http-route upstream %q: %w", strings.TrimSpace(upstreamRaw), err)
	}
	if upstream.Host == "" {
		return nil, fmt.Errorf("invalid --http-route upstream %q: %w", strings.TrimSpace(upstreamRaw), errors.New("upstream host is required"))
	}
	if upstream.Scheme != "http" && upstream.Scheme != "https" {
		return nil, fmt.Errorf("invalid --http-route upstream %q: %w", strings.TrimSpace(upstreamRaw), errors.New("upstream scheme must be http or https"))
	}
	upstream.Fragment = ""
	upstream.Path = utils.NormalizeURLPath(upstream.Path)

	return &httpRoute{
		prefix:   prefix,
		upstream: upstream,
	}, nil
}

func (r *httpRoute) newReverseProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite:        r.rewriteRequest,
		ModifyResponse: r.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			log.Error().
				Err(err).
				Str("route_prefix", r.prefix).
				Str("upstream", r.upstream.String()).
				Msg("http route proxy failed")
			http.Error(w, "bad gateway", http.StatusBadGateway)
		},
	}
}

func (r *httpRoute) rewriteRequest(pr *httputil.ProxyRequest) {
	outboundPath, outboundRawPath := r.stripPrefix(pr.In.URL.Path, pr.In.URL.RawPath)
	pr.Out.URL.Path = outboundPath
	pr.Out.URL.RawPath = outboundRawPath
	pr.Out.URL.RawQuery = pr.In.URL.RawQuery
	pr.SetURL(r.upstream)
	pr.SetXForwarded()
	if r.prefix != "/" {
		pr.Out.Header.Set("X-Forwarded-Prefix", r.prefix)
	}

	publicScheme := "http"
	if pr.In.TLS != nil {
		publicScheme = "https"
	} else {
		proto := strings.TrimSpace(pr.In.Header.Get("X-Forwarded-Proto"))
		if first, _, ok := strings.Cut(proto, ","); ok {
			proto = first
		}
		if proto = strings.ToLower(strings.TrimSpace(proto)); proto != "" {
			publicScheme = proto
		}
	}

	meta := httpRouteMeta{
		publicHost:   pr.In.Host,
		publicScheme: publicScheme,
	}
	pr.Out = pr.Out.WithContext(context.WithValue(pr.Out.Context(), httpRouteMetaKey{}, meta))
}

func (r *httpRoute) modifyResponse(resp *http.Response) error {
	if resp == nil || resp.Request == nil {
		return nil
	}

	meta, _ := resp.Request.Context().Value(httpRouteMetaKey{}).(httpRouteMeta)
	r.rewriteLocation(resp.Header, meta)
	r.rewriteSetCookies(resp.Header, meta.publicHost)
	return nil
}

func (r *httpRoute) stripPrefix(requestPath, rawPath string) (string, string) {
	requestPath = utils.NormalizeURLPath(requestPath)
	if r.prefix == "/" {
		return requestPath, rawPath
	}
	if requestPath == r.prefix {
		return "/", ""
	}

	trimmedPath := strings.TrimPrefix(requestPath, r.prefix)
	if trimmedPath == "" {
		trimmedPath = "/"
	}

	trimmedRawPath := rawPath
	if trimmedRawPath != "" {
		if trimmedRawPath == r.prefix {
			trimmedRawPath = "/"
		} else if strings.HasPrefix(trimmedRawPath, r.prefix+"/") {
			trimmedRawPath = strings.TrimPrefix(trimmedRawPath, r.prefix)
		}
	}
	return trimmedPath, trimmedRawPath
}

func (r *httpRoute) rewriteLocation(header http.Header, meta httpRouteMeta) {
	location := strings.TrimSpace(header.Get("Location"))
	if location == "" {
		return
	}

	parsed, err := url.Parse(location)
	if err != nil {
		return
	}

	var mappedPath string
	switch {
	case parsed.IsAbs():
		if !strings.EqualFold(parsed.Scheme, r.upstream.Scheme) || !strings.EqualFold(parsed.Host, r.upstream.Host) {
			return
		}
		parsed.Scheme = meta.publicScheme
		parsed.Host = meta.publicHost
	case strings.HasPrefix(location, "/") && (len(location) == 1 || (location[1] != '/' && location[1] != '\\')):
	default:
		return
	}

	mappedPath = r.mapUpstreamPathToPublic(parsed.Path)
	if !strings.HasPrefix(mappedPath, "/") || (len(mappedPath) > 1 && (mappedPath[1] == '/' || mappedPath[1] == '\\')) {
		return
	}
	parsed.Path = mappedPath
	parsed.RawPath = ""
	header.Set("Location", parsed.String())
}

func (r *httpRoute) rewriteSetCookies(header http.Header, publicHost string) {
	values := header.Values("Set-Cookie")
	if len(values) == 0 {
		return
	}

	publicDomain := strings.ToLower(strings.TrimSpace(publicHost))
	if host, port, err := net.SplitHostPort(publicDomain); err == nil && port != "" {
		publicDomain = host
	}
	publicDomain = strings.Trim(publicDomain, "[]")
	upstreamDomain := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(r.upstream.Hostname()), "."))
	header.Del("Set-Cookie")
	for _, value := range values {
		cookie, err := http.ParseSetCookie(value)
		if err != nil {
			header.Add("Set-Cookie", value)
			continue
		}

		changed := false
		if strings.TrimSpace(cookie.Path) != "" {
			rewrittenPath := r.mapUpstreamPathToPublic(cookie.Path)
			if rewrittenPath != cookie.Path {
				cookie.Path = rewrittenPath
				changed = true
			}
		}

		currentDomain := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(cookie.Domain), "."))
		if currentDomain != "" && currentDomain != publicDomain &&
			(currentDomain == upstreamDomain || utils.IsLocalRelayHost(currentDomain)) {
			cookie.Domain = ""
			changed = true
		}

		if changed {
			header.Add("Set-Cookie", cookie.String())
			continue
		}
		header.Add("Set-Cookie", value)
	}
}

func (r *httpRoute) mapUpstreamPathToPublic(raw string) string {
	raw = utils.NormalizeURLPath(raw)
	if r.prefix != "/" && (raw == r.prefix || strings.HasPrefix(raw, r.prefix+"/")) {
		return raw
	}

	base := utils.NormalizeURLPath(r.upstream.Path)
	publicRest := raw
	switch {
	case base == "/":
	case raw == base:
		publicRest = "/"
	case strings.HasPrefix(raw, base+"/"):
		publicRest = strings.TrimPrefix(raw, base)
	}

	if r.prefix == "/" {
		return publicRest
	}
	if publicRest == "/" {
		return r.prefix
	}
	if strings.HasPrefix(publicRest, "/") {
		return r.prefix + publicRest
	}
	return r.prefix + "/" + publicRest
}
