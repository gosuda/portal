package sdk

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// resolvePublicIP attempts to determine the caller's public IP address
// using well-known external services. Returns empty string on failure.
// Best-effort with a short timeout to avoid blocking registration.
func resolvePublicIP(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	endpoints := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
	}

	for _, endpoint := range endpoints {
		if ip := queryIPEndpoint(ctx, endpoint); ip != "" {
			return ip
		}
	}
	return ""
}

func queryIPEndpoint(ctx context.Context, url string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "portal-tunnel")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return ""
	}

	candidate := strings.TrimSpace(string(body))
	if net.ParseIP(candidate) == nil {
		return ""
	}
	return candidate
}
