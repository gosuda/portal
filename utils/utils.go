package utils

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

func SplitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func NormalizeRelayURLs(inputs []string) ([]string, error) {
	out := make([]string, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))

	for _, input := range inputs {
		for _, part := range SplitCSV(input) {
			normalized, err := NormalizeRelayURL(part)
			if err != nil {
				return nil, err
			}
			if _, ok := seen[normalized]; ok {
				continue
			}
			seen[normalized] = struct{}{}
			out = append(out, normalized)
		}
	}

	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func NormalizeRelayURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("relay url is empty")
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + strings.TrimPrefix(trimmed, "//")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse relay url %q: %w", raw, err)
	}
	if parsed.Host == "" && parsed.Path != "" && !strings.Contains(parsed.Path, "/") {
		parsed, err = url.Parse("https://" + strings.TrimSpace(parsed.Path))
		if err != nil {
			return "", fmt.Errorf("parse relay url %q: %w", raw, err)
		}
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("relay url host is empty: %q", raw)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("relay url must use https: %q", raw)
	}

	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(strings.ToLower(parsed.Path), "/relay") {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/relay")
	}
	return parsed.String(), nil
}

func NormalizeTargetAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("target address is required")
	}

	if strings.Contains(raw, "://") {
		targetURL, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("parse target url: %w", err)
		}
		if !strings.EqualFold(targetURL.Scheme, "http") && !strings.EqualFold(targetURL.Scheme, "https") {
			return "", fmt.Errorf("unsupported target url scheme %q", targetURL.Scheme)
		}
		if targetURL.Host == "" {
			return "", errors.New("target url host is empty")
		}
		if targetURL.Path != "" && targetURL.Path != "/" {
			return "", errors.New("target url path is not supported")
		}
		if targetURL.RawQuery != "" {
			return "", errors.New("target url query is not supported")
		}
		if targetURL.Fragment != "" {
			return "", errors.New("target url fragment is not supported")
		}
		raw = targetURL.Host
	}

	if _, _, err := net.SplitHostPort(raw); err == nil {
		return raw, nil
	}
	if strings.Count(raw, ":") == 0 {
		return net.JoinHostPort(raw, "80"), nil
	}
	if ip := net.ParseIP(raw); ip != nil {
		return net.JoinHostPort(raw, "80"), nil
	}
	return "", fmt.Errorf("invalid target address %q", raw)
}

func PortalRootHost(portalURL string) string {
	u, err := url.Parse(strings.TrimSpace(portalURL))
	if err != nil || u.Host == "" {
		return ""
	}
	return NormalizeHostname(u.Hostname())
}

func NormalizeHostname(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	return host
}

func HostPortOrLoopback(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func EnsurePort(host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, "443")
}

func IsLocalRelayHost(host string) bool {
	host = NormalizeHostname(host)
	switch host {
	case "", "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.HasSuffix(host, ".localhost")
}

func DurationOrDefault(v, fallback time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return fallback
}

func IntOrDefault(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

func RandomID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(buf)
}
