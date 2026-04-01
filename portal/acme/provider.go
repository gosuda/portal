package acme

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-acme/lego/v4/challenge"

	"github.com/gosuda/portal/v2/portal/acme/cloudflare"
	"github.com/gosuda/portal/v2/portal/acme/route53"
	"github.com/gosuda/portal/v2/types"
)

const (
	TypeCloudflare = "cloudflare"
	TypeRoute53    = "route53"
)

type DNSProviderConfig struct {
	Type               string
	CloudflareToken    string
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSSessionToken    string
	AWSRegion          string
	AWSHostedZoneID    string
	AWSKMSKeyARN       string
	DNSSECKSKName      string
}

type DNSProvider interface {
	Name() string
	ChallengeProvider(ctx context.Context) (challenge.Provider, error)
	EnsureARecords(ctx context.Context, baseDomain, publicIPv4 string) error
	EnsureTXTRecord(ctx context.Context, name, value string) error
	DeleteTXTRecords(ctx context.Context, name, matchPrefix string) error
	EnsureDNSSEC(ctx context.Context, baseDomain string) (types.DNSSECStatus, error)
}

func NewDNSProvider(cfg DNSProviderConfig) (DNSProvider, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case TypeCloudflare:
		return cloudflare.New(cfg.CloudflareToken), nil
	case TypeRoute53:
		return route53.New(route53.Config{
			AccessKeyID:     cfg.AWSAccessKeyID,
			SecretAccessKey: cfg.AWSSecretAccessKey,
			SessionToken:    cfg.AWSSessionToken,
			Region:          cfg.AWSRegion,
			HostedZoneID:    cfg.AWSHostedZoneID,
			KMSKeyARN:       cfg.AWSKMSKeyARN,
			DNSSECKSKName:   cfg.DNSSECKSKName,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported acme dns provider: %q", cfg.Type)
	}
}
