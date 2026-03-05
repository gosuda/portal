package acme

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/types"
)

const (
	cfAPIBase      = "https://api.cloudflare.com/client/v4"
	publicIPURL    = "https://api4.ipify.org"
	dnsHTTPTimeout = 15 * time.Second
	dnsAutoTTL     = 1 // Cloudflare "automatic" TTL
)

// Cloudflare API response types.

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cfZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cfDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

type cfZonesResult struct {
	Success bool      `json:"success"`
	Errors  []cfError `json:"errors"`
	Result  []cfZone  `json:"result"`
}

type cfRecordsResult struct {
	Success bool          `json:"success"`
	Errors  []cfError     `json:"errors"`
	Result  []cfDNSRecord `json:"result"`
}

type cfRecordResult struct {
	Success bool        `json:"success"`
	Errors  []cfError   `json:"errors"`
	Result  cfDNSRecord `json:"result"`
}

// EnsureDNSRecords creates or updates Cloudflare A records for the base domain
// and its wildcard subdomain, pointing to the server's detected public IP.
// Skips silently when baseDomain is empty, localhost, or cloudflareToken is missing.
func EnsureDNSRecords(ctx context.Context, baseDomain, cloudflareToken string) error {
	baseDomain = strings.TrimSpace(baseDomain)
	cloudflareToken = strings.TrimSpace(cloudflareToken)

	if baseDomain == "" || cloudflareToken == "" || types.IsLocalhost(baseDomain) {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	publicIP, err := detectPublicIP(ctx)
	if err != nil {
		return fmt.Errorf("detect public IP: %w", err)
	}

	log.Info().
		Str("public_ip", publicIP).
		Str("base_domain", baseDomain).
		Msg("[DNS] detected server public IP")

	zoneID, err := findZoneID(ctx, cloudflareToken, baseDomain)
	if err != nil {
		return fmt.Errorf("find Cloudflare zone for %s: %w", baseDomain, err)
	}

	targets := []string{baseDomain, "*." + baseDomain}
	for _, name := range targets {
		if err := ensureARecord(ctx, cloudflareToken, zoneID, name, publicIP); err != nil {
			return fmt.Errorf("ensure A record for %s: %w", name, err)
		}
	}

	return nil
}

// detectPublicIP fetches the server's public IPv4 address from an external service.
func detectPublicIP(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, dnsHTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, publicIPURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}

	ip := strings.TrimSpace(string(body))
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return "", fmt.Errorf("invalid IP address: %q", ip)
	}
	if parsed.To4() == nil {
		return "", fmt.Errorf("expected IPv4 address, got: %q", ip)
	}

	return ip, nil
}

// findZoneID looks up the Cloudflare zone ID by progressively stripping
// subdomain labels from the given domain (e.g., portal.example.com → example.com).
func findZoneID(ctx context.Context, token, domain string) (string, error) {
	parts := strings.Split(domain, ".")
	for i := range len(parts) - 1 {
		candidate := strings.Join(parts[i:], ".")

		zones, err := cfListZones(ctx, token, candidate)
		if err != nil {
			return "", err
		}
		for _, z := range zones {
			if strings.EqualFold(z.Name, candidate) {
				log.Debug().
					Str("zone", z.Name).
					Str("zone_id", z.ID).
					Msg("[DNS] found Cloudflare zone")
				return z.ID, nil
			}
		}
	}

	return "", fmt.Errorf("no Cloudflare zone found for domain %s", domain)
}

// ensureARecord creates or updates a single A record.
// If the record exists with the correct IP and proxy-off, it is left untouched.
func ensureARecord(ctx context.Context, token, zoneID, name, ip string) error {
	records, err := cfListDNSRecords(ctx, token, zoneID, name, "A")
	if err != nil {
		return err
	}

	for _, r := range records {
		if !strings.EqualFold(r.Name, name) {
			continue
		}
		if r.Content == ip && !r.Proxied {
			log.Info().
				Str("name", name).
				Str("ip", ip).
				Msg("[DNS] A record already up to date")
			return nil
		}
		// Record exists but IP or proxy status differs — update it.
		return cfUpdateDNSRecord(ctx, token, zoneID, r.ID, name, ip)
	}

	return cfCreateDNSRecord(ctx, token, zoneID, name, ip)
}

// ── Cloudflare API helpers ──────────────────────────────────────────

func cfListZones(ctx context.Context, token, name string) ([]cfZone, error) {
	u, _ := url.Parse(cfAPIBase + "/zones")
	q := u.Query()
	q.Set("name", name)
	u.RawQuery = q.Encode()

	var out cfZonesResult
	if err := cfGet(ctx, token, u.String(), &out); err != nil {
		return nil, err
	}
	if !out.Success {
		return nil, cfErrs(out.Errors)
	}
	return out.Result, nil
}

func cfListDNSRecords(ctx context.Context, token, zoneID, name, recordType string) ([]cfDNSRecord, error) {
	u, _ := url.Parse(fmt.Sprintf("%s/zones/%s/dns_records", cfAPIBase, zoneID))
	q := u.Query()
	q.Set("name", name)
	q.Set("type", recordType)
	u.RawQuery = q.Encode()

	var out cfRecordsResult
	if err := cfGet(ctx, token, u.String(), &out); err != nil {
		return nil, err
	}
	if !out.Success {
		return nil, cfErrs(out.Errors)
	}
	return out.Result, nil
}

func cfCreateDNSRecord(ctx context.Context, token, zoneID, name, ip string) error {
	endpoint := fmt.Sprintf("%s/zones/%s/dns_records", cfAPIBase, zoneID)

	body := map[string]any{
		"type":    "A",
		"name":    name,
		"content": ip,
		"ttl":     dnsAutoTTL,
		"proxied": false,
	}

	var out cfRecordResult
	if err := cfMutate(ctx, http.MethodPost, token, endpoint, body, &out); err != nil {
		return err
	}
	if !out.Success {
		return cfErrs(out.Errors)
	}

	log.Info().
		Str("name", name).
		Str("ip", ip).
		Msg("[DNS] created A record")
	return nil
}

func cfUpdateDNSRecord(ctx context.Context, token, zoneID, recordID, name, ip string) error {
	endpoint := fmt.Sprintf("%s/zones/%s/dns_records/%s", cfAPIBase, zoneID, recordID)

	body := map[string]any{
		"type":    "A",
		"name":    name,
		"content": ip,
		"ttl":     dnsAutoTTL,
		"proxied": false,
	}

	var out cfRecordResult
	if err := cfMutate(ctx, http.MethodPut, token, endpoint, body, &out); err != nil {
		return err
	}
	if !out.Success {
		return cfErrs(out.Errors)
	}

	log.Info().
		Str("name", name).
		Str("ip", ip).
		Msg("[DNS] updated A record")
	return nil
}

// ── HTTP transport ──────────────────────────────────────────────────

func cfGet(ctx context.Context, token, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return json.NewDecoder(resp.Body).Decode(out)
}

func cfMutate(ctx context.Context, method, token, rawURL string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return json.NewDecoder(resp.Body).Decode(out)
}

func cfErrs(errs []cfError) error {
	if len(errs) == 0 {
		return fmt.Errorf("cloudflare API request failed")
	}
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, fmt.Sprintf("[%d] %s", e.Code, e.Message))
	}
	return fmt.Errorf("cloudflare API: %s", strings.Join(msgs, "; "))
}
