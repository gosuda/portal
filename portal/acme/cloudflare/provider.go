package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"

	"github.com/gosuda/portal/v2/utils"
)

const (
	apiBase = "https://api.cloudflare.com/client/v4"
)

type Provider struct {
	token string
}

type apiError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

type zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type dnsRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

type zonesResult struct {
	Errors  []apiError `json:"errors"`
	Result  []zone     `json:"result"`
	Success bool       `json:"success"`
}

type recordsResult struct {
	Errors  []apiError  `json:"errors"`
	Result  []dnsRecord `json:"result"`
	Success bool        `json:"success"`
}

type recordResult struct {
	Result  dnsRecord  `json:"result"`
	Errors  []apiError `json:"errors"`
	Success bool       `json:"success"`
}

func New(token string) *Provider {
	return &Provider{token: strings.TrimSpace(token)}
}

func (p *Provider) Name() string {
	return "cloudflare"
}

func (p *Provider) ChallengeProvider(context.Context) (challenge.Provider, error) {
	if p == nil {
		return nil, errors.New("cloudflare provider is nil")
	}
	if p.token == "" {
		return nil, errors.New("cloudflare token is required")
	}

	cfg := cloudflare.NewDefaultConfig()
	cfg.AuthToken = p.token

	provider, err := cloudflare.NewDNSProviderConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create cloudflare lego provider: %w", err)
	}
	return provider, nil
}

func (p *Provider) EnsureARecords(ctx context.Context, baseDomain, publicIPv4 string) error {
	if p == nil {
		return errors.New("cloudflare provider is nil")
	}
	baseDomain = strings.TrimPrefix(utils.NormalizeHostname(baseDomain), "*.")
	if baseDomain == "" {
		return errors.New("base domain is required")
	}
	if p.token == "" {
		return errors.New("cloudflare token is required")
	}
	publicIPv4 = strings.TrimSpace(publicIPv4)
	if publicIPv4 == "" {
		return errors.New("public ipv4 is required")
	}

	zoneID, err := findZoneID(ctx, p.token, baseDomain)
	if err != nil {
		return fmt.Errorf("find cloudflare zone: %w", err)
	}

	for _, name := range []string{baseDomain, "*." + baseDomain} {
		if err := ensureARecord(ctx, p.token, zoneID, name, publicIPv4); err != nil {
			return fmt.Errorf("ensure A record for %s: %w", name, err)
		}
	}
	return nil
}

func findZoneID(ctx context.Context, token, domain string) (string, error) {
	parts := strings.Split(domain, ".")
	for i := range len(parts) - 1 {
		candidate := strings.Join(parts[i:], ".")
		zones, err := listZones(ctx, token, candidate)
		if err != nil {
			return "", err
		}
		for _, z := range zones {
			if strings.EqualFold(z.Name, candidate) {
				return z.ID, nil
			}
		}
	}
	return "", fmt.Errorf("no cloudflare zone found for %s", domain)
}

func ensureARecord(ctx context.Context, token, zoneID, name, ip string) error {
	records, err := listDNSRecords(ctx, token, zoneID, name, "A")
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
		return updateDNSRecord(ctx, token, zoneID, record.ID, name, ip)
	}

	return createDNSRecord(ctx, token, zoneID, name, ip)
}

func listZones(ctx context.Context, token, name string) ([]zone, error) {
	u, _ := url.Parse(apiBase + "/zones")
	q := u.Query()
	q.Set("name", name)
	u.RawQuery = q.Encode()

	var out zonesResult
	if err := utils.HTTPDoJSON(ctx, nil, http.MethodGet, u.String(), nil, cloudflareHeaders(token), &out); err != nil {
		return nil, err
	}
	if !out.Success {
		return nil, wrapErrors(out.Errors)
	}
	return out.Result, nil
}

func listDNSRecords(ctx context.Context, token, zoneID, name, recordType string) ([]dnsRecord, error) {
	u, _ := url.Parse(fmt.Sprintf("%s/zones/%s/dns_records", apiBase, zoneID))
	q := u.Query()
	q.Set("name", name)
	q.Set("type", recordType)
	u.RawQuery = q.Encode()

	var out recordsResult
	if err := utils.HTTPDoJSON(ctx, nil, http.MethodGet, u.String(), nil, cloudflareHeaders(token), &out); err != nil {
		return nil, err
	}
	if !out.Success {
		return nil, wrapErrors(out.Errors)
	}
	return out.Result, nil
}

func createDNSRecord(ctx context.Context, token, zoneID, name, ip string) error {
	endpoint := fmt.Sprintf("%s/zones/%s/dns_records", apiBase, zoneID)
	body := map[string]any{
		"type":    "A",
		"name":    name,
		"content": ip,
		"ttl":     1,
		"proxied": false,
	}

	var out recordResult
	if err := utils.HTTPDoJSON(ctx, nil, http.MethodPost, endpoint, body, cloudflareHeaders(token), &out); err != nil {
		return err
	}
	if !out.Success {
		return wrapErrors(out.Errors)
	}
	return nil
}

func updateDNSRecord(ctx context.Context, token, zoneID, recordID, name, ip string) error {
	endpoint := fmt.Sprintf("%s/zones/%s/dns_records/%s", apiBase, zoneID, recordID)
	body := map[string]any{
		"type":    "A",
		"name":    name,
		"content": ip,
		"ttl":     1,
		"proxied": false,
	}

	var out recordResult
	if err := utils.HTTPDoJSON(ctx, nil, http.MethodPut, endpoint, body, cloudflareHeaders(token), &out); err != nil {
		return err
	}
	if !out.Success {
		return wrapErrors(out.Errors)
	}
	return nil
}

func cloudflareHeaders(token string) http.Header {
	return http.Header{
		"Authorization": []string{"Bearer " + token},
		"Content-Type":  []string{"application/json"},
	}
}

func wrapErrors(errs []apiError) error {
	if len(errs) == 0 {
		return errors.New("cloudflare api request failed")
	}
	messages := make([]string, 0, len(errs))
	for _, apiErr := range errs {
		messages = append(messages, fmt.Sprintf("[%d] %s", apiErr.Code, apiErr.Message))
	}
	return errors.New(strings.Join(messages, "; "))
}
