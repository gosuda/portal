package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

const defaultRequestTimeout = 15 * time.Second

func NormalizeDescriptor(desc types.RelayDescriptor) (types.RelayDescriptor, error) {
	desc.Name = utils.NormalizeHostname(desc.Name)
	desc.Address = strings.TrimSpace(desc.Address)
	desc.APIHTTPSAddr = strings.TrimSpace(desc.APIHTTPSAddr)
	if !desc.IssuedAt.IsZero() {
		desc.IssuedAt = desc.IssuedAt.UTC()
	}
	if !desc.ExpiresAt.IsZero() {
		desc.ExpiresAt = desc.ExpiresAt.UTC()
	}

	if desc.APIHTTPSAddr != "" {
		normalized, err := utils.NormalizeRelayURL(desc.APIHTTPSAddr)
		if err != nil {
			return types.RelayDescriptor{}, fmt.Errorf("normalize api https addr: %w", err)
		}
		desc.APIHTTPSAddr = normalized
	}
	if desc.Address != "" {
		normalized, err := utils.NormalizeEVMAddress(desc.Address)
		if err != nil {
			return types.RelayDescriptor{}, fmt.Errorf("normalize address: %w", err)
		}
		desc.Address = normalized
	}
	return desc, nil
}

func ValidateDescriptor(desc types.RelayDescriptor, now time.Time) (types.RelayDescriptor, error) {
	normalized, err := NormalizeDescriptor(desc)
	if err != nil {
		return types.RelayDescriptor{}, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	switch {
	case normalized.Name == "":
		return types.RelayDescriptor{}, errors.New("identity.name is required")
	case normalized.APIHTTPSAddr == "":
		return types.RelayDescriptor{}, errors.New("api_https_addr is required")
	case normalized.Sequence == 0:
		return types.RelayDescriptor{}, errors.New("sequence is required")
	case normalized.Version == 0:
		return types.RelayDescriptor{}, errors.New("version is required")
	case normalized.IssuedAt.IsZero():
		return types.RelayDescriptor{}, errors.New("issued_at is required")
	case normalized.ExpiresAt.IsZero():
		return types.RelayDescriptor{}, errors.New("expires_at is required")
	case normalized.ExpiresAt.Before(now):
		return types.RelayDescriptor{}, errors.New("descriptor expired")
	case normalized.IssuedAt.After(normalized.ExpiresAt):
		return types.RelayDescriptor{}, errors.New("issued_at must be before expires_at")
	}
	return normalized, nil
}

func ValidateRelayDiscoveryResponse(resp types.DiscoveryResponse, now time.Time) (types.RelayDescriptor, []types.RelayDescriptor, error) {
	protocolVersion := strings.TrimSpace(resp.ProtocolVersion)
	if protocolVersion != types.ProtocolVersion {
		return types.RelayDescriptor{}, nil, fmt.Errorf("relay protocol version mismatch: relay=%q client=%q", protocolVersion, types.ProtocolVersion)
	}

	self, err := ValidateDescriptor(resp.Self, now)
	if err != nil {
		return types.RelayDescriptor{}, nil, err
	}

	seen := map[string]struct{}{self.Key(): {}}
	relays := make([]types.RelayDescriptor, 0, len(resp.Relays))
	for _, descriptor := range resp.Relays {
		verified, err := ValidateDescriptor(descriptor, now)
		if err != nil {
			log.Warn().
				Err(err).
				Str("relay", strings.TrimSpace(descriptor.APIHTTPSAddr)).
				Str("name", strings.TrimSpace(descriptor.Name)).
				Msg("skipping invalid discovery relay hint")
			continue
		}
		identityKey := verified.Key()
		if _, ok := seen[identityKey]; ok {
			log.Debug().
				Str("relay", verified.APIHTTPSAddr).
				Str("identity_key", identityKey).
				Msg("skipping duplicate discovery relay hint")
			continue
		}
		seen[identityKey] = struct{}{}
		relays = append(relays, verified)
	}
	return self, relays, nil
}

// ValidateDescriptorTarget checks if a descriptor matches expected target identity.
func ValidateDescriptorTarget(desc types.RelayDescriptor, targetIdentity types.Identity, targetURL string) error {
	normalized, err := NormalizeDescriptor(desc)
	if err != nil {
		return err
	}

	targetName := strings.TrimSpace(targetIdentity.Name)
	if targetName != "" {
		normalizedTargetName := utils.NormalizeHostname(targetName)
		if normalized.Name != normalizedTargetName {
			return errors.New("descriptor name does not match target relay")
		}
	}
	targetAddress := strings.TrimSpace(targetIdentity.Address)
	if targetAddress != "" {
		normalizedTargetAddress, err := utils.NormalizeEVMAddress(targetAddress)
		if err != nil {
			return err
		}
		if normalized.Address != normalizedTargetAddress {
			return errors.New("descriptor address does not match target relay")
		}
	}

	if targetURL != "" {
		normalizedTargetURL, err := utils.NormalizeRelayURL(targetURL)
		if err != nil {
			return err
		}
		if normalized.APIHTTPSAddr != normalizedTargetURL {
			return errors.New("descriptor api_https_addr does not match target url")
		}
	}
	return nil
}

func DiscoverRelayDiscovery(ctx context.Context, baseURL string, rootCAPEM []byte, httpClient *http.Client) (types.DiscoveryResponse, error) {
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return types.DiscoveryResponse{}, fmt.Errorf("parse discovery base url: %w", err)
	}

	client := httpClient
	if client == nil {
		_, client, err = keyless.NewRelayHTTPClient(ctx, parsedBaseURL, rootCAPEM, defaultRequestTimeout)
		if err != nil {
			return types.DiscoveryResponse{}, err
		}
	}
	if client.Timeout == 0 {
		clone := *client
		clone.Timeout = defaultRequestTimeout
		client = &clone
	}

	var resp types.DiscoveryResponse
	if err := utils.HTTPDoAPIPath(ctx, client, parsedBaseURL, http.MethodGet, types.PathDiscovery, nil, nil, &resp); err != nil {
		return types.DiscoveryResponse{}, err
	}
	return resp, nil
}

func DiscoveryUnavailableStatus(err error) (statusCode int, code string, unavailable bool) {
	var apiErr *types.APIRequestError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return 0, "", false
	}
	code = strings.TrimSpace(apiErr.Code)
	if apiErr.StatusCode == http.StatusNotFound || code == types.APIErrorCodeFeatureUnavailable {
		return apiErr.StatusCode, code, true
	}
	return 0, "", false
}
