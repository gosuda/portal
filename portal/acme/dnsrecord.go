package acme

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	cfAPIBase      = "https://api.cloudflare.com/client/v4"
	publicIPURL    = "https://api4.ipify.org"
	dnsHTTPTimeout = 15 * time.Second
	dnsAutoTTL     = 1
)

type cfError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
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
}

type cfZonesResult struct {
	Errors  []cfError `json:"errors"`
	Result  []cfZone  `json:"result"`
	Success bool      `json:"success"`
}

type cfRecordsResult struct {
	Errors  []cfError     `json:"errors"`
	Result  []cfDNSRecord `json:"result"`
	Success bool          `json:"success"`
}

type cfRecordResult struct {
	Errors  []cfError   `json:"errors"`
	Result  cfDNSRecord `json:"result"`
	Success bool        `json:"success"`
}

func EnsureDNSRecords(ctx context.Context, baseDomain, cloudflareToken string) error {
	baseDomain = normalizeHost(baseDomain)
	cloudflareToken = strings.TrimSpace(cloudflareToken)

	if baseDomain == "" || cloudflareToken == "" || isLocalhost(baseDomain) {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	publicIP, err := detectPublicIP(ctx)
	if err != nil {
		return fmt.Errorf("detect public ip: %w", err)
	}

	zoneID, err := findZoneID(ctx, cloudflareToken, baseDomain)
	if err != nil {
		return fmt.Errorf("find cloudflare zone: %w", err)
	}

	for _, name := range []string{baseDomain, "*." + baseDomain} {
		if err := ensureARecord(ctx, cloudflareToken, zoneID, name, publicIP); err != nil {
			return fmt.Errorf("ensure A record for %s: %w", name, err)
		}
	}
	return nil
}

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
	if parsed == nil || parsed.To4() == nil {
		return "", fmt.Errorf("invalid ipv4 address: %q", ip)
	}
	return ip, nil
}

func findZoneID(ctx context.Context, token, domain string) (string, error) {
	parts := strings.Split(domain, ".")
	for i := 0; i < len(parts)-1; i++ {
		candidate := strings.Join(parts[i:], ".")
		zones, err := cfListZones(ctx, token, candidate)
		if err != nil {
			return "", err
		}
		for _, zone := range zones {
			if strings.EqualFold(zone.Name, candidate) {
				return zone.ID, nil
			}
		}
	}
	return "", fmt.Errorf("no cloudflare zone found for %s", domain)
}

func ensureARecord(ctx context.Context, token, zoneID, name, ip string) error {
	records, err := cfListDNSRecords(ctx, token, zoneID, name, "A")
	if err != nil {
		return err
	}

	for _, record := range records {
		if !strings.EqualFold(record.Name, name) {
			continue
		}
		if record.Content == ip {
			return nil
		}
		return cfUpdateDNSRecord(ctx, token, zoneID, record.ID, name, ip)
	}
	return cfCreateDNSRecord(ctx, token, zoneID, name, ip)
}

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
	return nil
}

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
		return errors.New("cloudflare api request failed")
	}
	messages := make([]string, 0, len(errs))
	for _, cfErr := range errs {
		messages = append(messages, fmt.Sprintf("[%d] %s", cfErr.Code, cfErr.Message))
	}
	return errors.New(strings.Join(messages, "; "))
}
