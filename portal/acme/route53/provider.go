package route53

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awsroute53 "github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/providers/dns/route53"

	"github.com/gosuda/portal/v2/utils"
)

const defaultAWSRegion = "us-east-1"

type Config struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Region          string
	HostedZoneID    string
}

type Provider struct {
	cfg Config
}

func New(cfg Config) *Provider {
	return &Provider{
		cfg: Config{
			AccessKeyID:     strings.TrimSpace(cfg.AccessKeyID),
			SecretAccessKey: strings.TrimSpace(cfg.SecretAccessKey),
			SessionToken:    strings.TrimSpace(cfg.SessionToken),
			Region:          strings.TrimSpace(cfg.Region),
			HostedZoneID:    normalizeZoneID(cfg.HostedZoneID),
		},
	}
}

func (p *Provider) Name() string {
	return "route53"
}

func (p *Provider) ChallengeProvider(context.Context) (challenge.Provider, error) {
	if p == nil {
		return nil, errors.New("route53 provider is nil")
	}
	if err := validateConfig(p.cfg); err != nil {
		return nil, err
	}

	cfg := route53.NewDefaultConfig()
	cfg.AccessKeyID = p.cfg.AccessKeyID
	cfg.SecretAccessKey = p.cfg.SecretAccessKey
	cfg.SessionToken = p.cfg.SessionToken
	cfg.Region = p.awsRegion()
	cfg.HostedZoneID = p.cfg.HostedZoneID

	provider, err := route53.NewDNSProviderConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("create route53 lego provider: %w", err)
	}
	return provider, nil
}

func (p *Provider) EnsureARecords(ctx context.Context, baseDomain, publicIPv4 string) error {
	if p == nil {
		return errors.New("route53 provider is nil")
	}
	baseDomain = utils.NormalizeHostname(baseDomain)
	if baseDomain == "" {
		return errors.New("base domain is required")
	}
	if err := validateIPv4(publicIPv4); err != nil {
		return err
	}

	client, err := newClient(ctx, p.cfg)
	if err != nil {
		return err
	}

	hostedZoneID, err := findHostedZoneID(ctx, client, baseDomain, p.cfg.HostedZoneID)
	if err != nil {
		return err
	}

	for _, recordName := range []string{baseDomain, "*." + baseDomain} {
		if err := upsertARecord(ctx, client, hostedZoneID, recordName, publicIPv4); err != nil {
			return fmt.Errorf("upsert route53 A record %s: %w", recordName, err)
		}
	}
	return nil
}

func newClient(ctx context.Context, cfg Config) (*awsroute53.Client, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	loadOptions := []func(*config.LoadOptions) error{
		config.WithRegion(regionOrDefault(cfg.Region)),
	}
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		loadOptions = append(loadOptions, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return awsroute53.NewFromConfig(awsCfg), nil
}

func findHostedZoneID(ctx context.Context, client *awsroute53.Client, domain, explicitZoneID string) (string, error) {
	if explicitZoneID = normalizeZoneID(explicitZoneID); explicitZoneID != "" {
		return explicitZoneID, nil
	}
	if client == nil {
		return "", errors.New("route53 client is nil")
	}

	candidates := domainCandidates(domain)
	if len(candidates) == 0 {
		return "", fmt.Errorf("invalid base domain for hosted zone lookup: %q", domain)
	}

	zonesByName := make(map[string]string)
	paginator := awsroute53.NewListHostedZonesPaginator(client, &awsroute53.ListHostedZonesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("list hosted zones: %w", err)
		}
		for _, hostedZone := range page.HostedZones {
			if hostedZone.Config != nil && hostedZone.Config.PrivateZone {
				continue
			}
			zoneName := utils.NormalizeHostname(aws.ToString(hostedZone.Name))
			zoneID := normalizeZoneID(aws.ToString(hostedZone.Id))
			if zoneName == "" || zoneID == "" {
				continue
			}
			zonesByName[zoneName] = zoneID
		}
	}

	for _, candidate := range candidates {
		if zoneID, ok := zonesByName[candidate]; ok {
			return zoneID, nil
		}
	}

	return "", fmt.Errorf("no route53 public hosted zone found for %s", domain)
}

func upsertARecord(ctx context.Context, client *awsroute53.Client, hostedZoneID, name, ip string) error {
	if client == nil {
		return errors.New("route53 client is nil")
	}
	if hostedZoneID == "" {
		return errors.New("hosted zone id is required")
	}

	fqdn := ensureTrailingDot(utils.NormalizeHostname(name))
	recordSet := &types.ResourceRecordSet{
		Name: aws.String(fqdn),
		Type: types.RRTypeA,
		TTL:  aws.Int64(60),
		ResourceRecords: []types.ResourceRecord{
			{Value: aws.String(strings.TrimSpace(ip))},
		},
	}

	_, err := client.ChangeResourceRecordSets(ctx, &awsroute53.ChangeResourceRecordSetsInput{
		HostedZoneId: aws.String(hostedZoneID),
		ChangeBatch: &types.ChangeBatch{
			Comment: aws.String("Managed by Portal ACME"),
			Changes: []types.Change{
				{
					Action:            types.ChangeActionUpsert,
					ResourceRecordSet: recordSet,
				},
			},
		},
	})
	if err != nil {
		return err
	}
	return nil
}

func validateIPv4(raw string) error {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("invalid ipv4 address: %q", raw)
	}
	return nil
}

func domainCandidates(domain string) []string {
	parts := strings.Split(strings.TrimSpace(strings.TrimSuffix(domain, ".")), ".")
	if len(parts) < 2 {
		return nil
	}

	candidates := make([]string, 0, len(parts)-1)
	for i := range len(parts) - 1 {
		candidate := utils.NormalizeHostname(strings.Join(parts[i:], "."))
		if candidate != "" {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func (p *Provider) awsRegion() string {
	if p == nil {
		return defaultAWSRegion
	}
	return regionOrDefault(p.cfg.Region)
}

func regionOrDefault(region string) string {
	if trimmed := strings.TrimSpace(region); trimmed != "" {
		return trimmed
	}
	return defaultAWSRegion
}

func validateConfig(cfg Config) error {
	switch {
	case cfg.SessionToken != "" && (cfg.AccessKeyID == "" || cfg.SecretAccessKey == ""):
		return errors.New("route53 session token requires access key id and secret access key")
	case (cfg.AccessKeyID == "") != (cfg.SecretAccessKey == ""):
		return errors.New("route53 access key id and secret access key must be supplied together")
	}
	return nil
}

func normalizeZoneID(raw string) string {
	trimmed := strings.TrimSpace(raw)
	return strings.TrimPrefix(trimmed, "/hostedzone/")
}

func ensureTrailingDot(name string) string {
	if strings.HasSuffix(name, ".") {
		return name
	}
	return name + "."
}
