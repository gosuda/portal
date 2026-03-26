package utils

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gosuda/portal/v2/types"
)

// ResolvePublicIP attempts to determine the caller's public IP address
// using well-known external services. Returns empty string on failure.
// Best-effort with a short timeout to avoid blocking registration.
func ResolvePublicIP(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	endpoints := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
	}
	client := &http.Client{Timeout: 3 * time.Second}

	for _, endpoint := range endpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "portal-tunnel")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || readErr != nil {
			continue
		}

		if candidate := SanitizeReportedIP(string(body)); candidate != "" {
			return candidate
		}
	}

	return ""
}

func SanitizeReportedIP(raw string) string {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return ""
	}
	if net.ParseIP(candidate) == nil {
		return ""
	}
	return candidate
}

func ResolvePortalRelayURLs(ctx context.Context, explicit []string, includeDefaults bool) ([]string, error) {
	explicit, err := NormalizeRelayURLs(explicit...)
	if err != nil {
		return nil, err
	}
	if !includeDefaults {
		return explicit, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, types.PortalRelayRegistryURL, nil)
	if err != nil {
		return explicit, nil
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return explicit, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return explicit, nil
	}

	var registry struct {
		Relays []string `json:"relays"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&registry); err != nil {
		return explicit, nil
	}

	defaults, err := NormalizeRelayURLs(registry.Relays...)
	if err != nil {
		return explicit, nil
	}
	if len(defaults) == 0 {
		return explicit, nil
	}
	return MergeRelayURLs(defaults, nil, explicit)
}
