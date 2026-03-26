package discovery

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

type Resolver func(context.Context, types.DiscoverRequest) (types.DiscoverResponse, error)

const defaultRequestTimeout = 15 * time.Second

func NormalizeDescriptor(desc types.RelayDescriptor) (types.RelayDescriptor, error) {
	desc.RelayID = strings.TrimSpace(desc.RelayID)
	desc.SignerPublicKey = strings.ToLower(strings.TrimSpace(desc.SignerPublicKey))
	desc.APIHTTPSAddr = strings.TrimSpace(desc.APIHTTPSAddr)
	desc.WireGuardPublicKey = strings.TrimSpace(desc.WireGuardPublicKey)
	desc.WireGuardEndpoint = strings.TrimSpace(desc.WireGuardEndpoint)
	desc.OverlayIPv4 = strings.TrimSpace(desc.OverlayIPv4)
	desc.DescriptorSignature = strings.TrimSpace(desc.DescriptorSignature)
	if !desc.IssuedAt.IsZero() {
		desc.IssuedAt = desc.IssuedAt.UTC()
	}
	if !desc.ExpiresAt.IsZero() {
		desc.ExpiresAt = desc.ExpiresAt.UTC()
	}
	if !desc.LastMITMDetectedAt.IsZero() {
		desc.LastMITMDetectedAt = desc.LastMITMDetectedAt.UTC()
	}

	if desc.APIHTTPSAddr != "" {
		normalized, err := utils.NormalizeRelayURL(desc.APIHTTPSAddr)
		if err != nil {
			return types.RelayDescriptor{}, fmt.Errorf("normalize api https addr: %w", err)
		}
		desc.APIHTTPSAddr = normalized
		if desc.RelayID == "" {
			desc.RelayID = normalized
		}
	}
	if desc.OwnerAddress != "" {
		address, err := utils.NormalizeEVMAddress(desc.OwnerAddress)
		if err != nil {
			return types.RelayDescriptor{}, fmt.Errorf("normalize owner address: %w", err)
		}
		desc.OwnerAddress = address
	}
	if len(desc.OverlayCIDRs) > 0 {
		normalized, err := utils.NormalizeOverlayCIDRs(desc.OverlayCIDRs)
		if err != nil {
			return types.RelayDescriptor{}, err
		}
		desc.OverlayCIDRs = normalized
	}
	if !desc.SupportsOverlayPeer {
		desc.WireGuardPublicKey = ""
		desc.WireGuardEndpoint = ""
		desc.OverlayIPv4 = ""
		desc.OverlayCIDRs = nil
	}
	return desc, nil
}

func SignDescriptor(desc types.RelayDescriptor, privateKeyHex string) (string, error) {
	normalized, err := NormalizeDescriptor(desc)
	if err != nil {
		return "", err
	}
	normalized.DescriptorSignature = ""
	payload, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return utils.SignSHA256Secp256k1DER(payload, privateKeyHex)
}

func SignedDescriptor(desc types.RelayDescriptor, privateKeyHex string) (types.RelayDescriptor, error) {
	normalized, err := NormalizeDescriptor(desc)
	if err != nil {
		return types.RelayDescriptor{}, err
	}
	signature, err := SignDescriptor(normalized, privateKeyHex)
	if err != nil {
		return types.RelayDescriptor{}, err
	}
	normalized.DescriptorSignature = signature
	return normalized, nil
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
	case normalized.RelayID == "":
		return types.RelayDescriptor{}, errors.New("relay_id is required")
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

	derivedOwnerAddress, err := utils.AddressFromCompressedPublicKeyHex(normalized.SignerPublicKey)
	if err != nil {
		return types.RelayDescriptor{}, err
	}
	if normalized.OwnerAddress != derivedOwnerAddress {
		return types.RelayDescriptor{}, errors.New("owner_address does not match signer_public_key")
	}
	if normalized.SupportsOverlayPeer {
		if err := utils.ValidateWireGuardPublicKey(normalized.WireGuardPublicKey); err != nil {
			return types.RelayDescriptor{}, err
		}
		if err := utils.ValidateWireGuardEndpoint(normalized.WireGuardEndpoint); err != nil {
			return types.RelayDescriptor{}, err
		}
		if err := utils.ValidateOverlayIPv4(normalized.OverlayIPv4); err != nil {
			return types.RelayDescriptor{}, err
		}
	}

	signature := normalized.DescriptorSignature
	normalized.DescriptorSignature = ""
	payload, err := json.Marshal(normalized)
	if err != nil {
		return types.RelayDescriptor{}, err
	}
	if err := utils.VerifySHA256Secp256k1DER(payload, normalized.SignerPublicKey, signature); err != nil {
		return types.RelayDescriptor{}, err
	}
	normalized.DescriptorSignature = signature
	return normalized, nil
}

func ValidateResponse(resp types.DiscoverResponse, now time.Time) (types.RelayDescriptor, []types.RelayDescriptor, error) {
	self, err := ValidateDescriptor(resp.Self, now)
	if err != nil {
		return types.RelayDescriptor{}, nil, err
	}

	seen := map[string]struct{}{self.RelayID: {}}
	peers := make([]types.RelayDescriptor, 0, len(resp.Peers))
	var validateErr error
	for _, descriptor := range resp.Peers {
		verified, err := ValidateDescriptor(descriptor, now)
		if err != nil {
			validateErr = errors.Join(validateErr, fmt.Errorf("validate peer %q: %w", descriptor.RelayID, err))
			continue
		}
		if _, ok := seen[verified.RelayID]; ok {
			continue
		}
		seen[verified.RelayID] = struct{}{}
		peers = append(peers, verified)
	}
	return self, peers, validateErr
}

func Discover(ctx context.Context, baseURL string, req types.DiscoverRequest, rootCAPEM []byte, httpClient *http.Client) (types.DiscoverResponse, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return types.DiscoverResponse{}, errors.New("discovery base url is required")
	}

	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return types.DiscoverResponse{}, fmt.Errorf("parse discovery base url: %w", err)
	}
	if parsedBaseURL.Host == "" {
		return types.DiscoverResponse{}, errors.New("discovery base url host is required")
	}

	discoverURL := parsedBaseURL.ResolveReference(&url.URL{Path: types.PathDiscovery})
	query := discoverURL.Query()
	if req.RootHost != "" {
		query.Set("root_host", req.RootHost)
	}
	if req.Name != "" {
		query.Set("name", req.Name)
	}
	discoverURL.RawQuery = query.Encode()

	client := httpClient
	if client == nil {
		rootCAs, err := keyless.RelayRootCAs(ctx, baseURL, parsedBaseURL.Hostname(), rootCAPEM)
		if err != nil {
			return types.DiscoverResponse{}, err
		}
		client = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
					ServerName: parsedBaseURL.Hostname(),
					RootCAs:    rootCAs,
					NextProtos: []string{"http/1.1"},
				},
				ForceAttemptHTTP2: false,
			},
		}
	}
	if client.Timeout == 0 {
		clone := *client
		clone.Timeout = defaultRequestTimeout
		client = &clone
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, discoverURL.String(), nil)
	if err != nil {
		return types.DiscoverResponse{}, err
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return types.DiscoverResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return types.DiscoverResponse{}, utils.DecodeAPIRequestError(resp)
	}

	envelope, err := utils.DecodeAPIEnvelope[types.DiscoverResponse](resp.Body)
	if err != nil {
		return types.DiscoverResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if !envelope.OK {
		return types.DiscoverResponse{}, utils.NewAPIRequestError(resp.StatusCode, envelope.Error)
	}
	return envelope.Data, nil
}

func ServeHTTP(w http.ResponseWriter, r *http.Request, resolver Resolver) {
	if r.Method != http.MethodGet {
		utils.WriteAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}

	req := types.DiscoverRequest{
		RootHost: r.URL.Query().Get("root_host"),
		Name:     r.URL.Query().Get("name"),
	}
	req.RootHost = utils.NormalizeHostname(req.RootHost)
	req.Name = strings.TrimSpace(req.Name)
	if req.Name != "" {
		if req.RootHost == "" {
			utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, "root host is required when name is set")
			return
		}
		name, err := utils.NormalizeDNSLabel(req.Name)
		if err != nil {
			utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, err.Error())
			return
		}
		req.Name = name
	}
	if resolver == nil {
		utils.WriteAPIError(w, http.StatusInternalServerError, types.APIErrorCodeInternal, "discovery resolver is not configured")
		return
	}

	resp, err := resolver(r.Context(), req)
	if err != nil {
		utils.WriteAPIError(w, http.StatusInternalServerError, types.APIErrorCodeInternal, err.Error())
		return
	}
	utils.WriteAPIData(w, http.StatusOK, resp)
}
