package discovery

import (
	"context"
	"crypto/tls"
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

func Discover(ctx context.Context, relayURL string, req types.DiscoverRequest, rootCAPEM []byte) (types.DiscoverResponse, error) {
	return discoverPeer(ctx, relayURL, req, rootCAPEM)
}

func RelayAPIURLs(descriptors []types.RelayDescriptor) ([]string, error) {
	urls := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if apiURL := strings.TrimSpace(descriptor.APIHTTPSAddr); apiURL != "" {
			urls = append(urls, apiURL)
		}
	}
	if len(urls) == 0 {
		return nil, nil
	}

	normalized, err := utils.NormalizeRelayURLs(urls...)
	if err != nil {
		return nil, err
	}
	return utils.ExcludeLocalRelayURLs(normalized...)
}

func ResolvePeerResponse(resp types.DiscoverResponse, now time.Time) (types.RelayDescriptor, []types.RelayDescriptor, error) {
	self, err := ValidateDescriptor(resp.Self, now)
	if err != nil {
		return types.RelayDescriptor{}, nil, fmt.Errorf("validate self descriptor: %w", err)
	}

	seen := map[string]struct{}{self.RelayID: {}}
	peers := make([]types.RelayDescriptor, 0, len(resp.Peers))
	var resolveErr error

	for _, descriptor := range resp.Peers {
		verified, err := ValidateDescriptor(descriptor, now)
		if err != nil {
			resolveErr = errors.Join(resolveErr, fmt.Errorf("validate peer %q: %w", descriptor.RelayID, err))
			continue
		}
		if _, ok := seen[verified.RelayID]; ok {
			continue
		}
		seen[verified.RelayID] = struct{}{}
		peers = append(peers, verified)
	}

	return self, peers, resolveErr
}

func DiscoverBootstraps(ctx context.Context, peers []string, req types.DiscoverRequest, rootCAPEM []byte) ([]string, error) {
	peers, err := utils.ExcludeLocalRelayURLs(peers...)
	if err != nil {
		return nil, err
	}
	if len(peers) == 0 {
		return nil, nil
	}

	req, err = normalizeRequest(req)
	if err != nil {
		return nil, err
	}

	bootstraps := append([]string(nil), peers...)
	var discoverErr error
	discovered := false

	for _, peer := range peers {
		resp, err := Discover(ctx, peer, req, rootCAPEM)
		if err != nil {
			discoverErr = errors.Join(discoverErr, fmt.Errorf("discover %q: %w", peer, err))
			continue
		}

		self, advertised, resolveErr := ResolvePeerResponse(resp, time.Now().UTC())
		if strings.TrimSpace(self.RelayID) == "" {
			discoverErr = errors.Join(discoverErr, fmt.Errorf("resolve %q self descriptor: %w", peer, resolveErr))
			continue
		}
		if resolveErr != nil {
			discoverErr = errors.Join(discoverErr, fmt.Errorf("resolve %q descriptors: %w", peer, resolveErr))
		}

		descriptors := append([]types.RelayDescriptor{self}, advertised...)
		discoveredBootstraps, err := RelayAPIURLs(descriptors)
		if err != nil {
			discoverErr = errors.Join(discoverErr, fmt.Errorf("extract %q relay urls: %w", peer, err))
			continue
		}
		bootstraps, err = utils.MergeRelayURLs(bootstraps, nil, discoveredBootstraps)
		if err != nil {
			discoverErr = errors.Join(discoverErr, fmt.Errorf("merge %q bootstraps: %w", peer, err))
			continue
		}
		discovered = true
	}

	if !discovered {
		return bootstraps, discoverErr
	}
	return bootstraps, discoverErr
}

func ServeHTTP(w http.ResponseWriter, r *http.Request, resolver Resolver) {
	if r.Method != http.MethodGet {
		utils.WriteAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}

	req, err := normalizeRequest(types.DiscoverRequest{
		RootHost: r.URL.Query().Get("root_host"),
		Name:     r.URL.Query().Get("name"),
	})
	if err != nil {
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, err.Error())
		return
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

func normalizeRequest(req types.DiscoverRequest) (types.DiscoverRequest, error) {
	req.RootHost = utils.NormalizeHostname(req.RootHost)
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return req, nil
	}
	if req.RootHost == "" {
		return types.DiscoverRequest{}, errors.New("root host is required when name is set")
	}
	name, err := utils.NormalizeDNSLabel(req.Name)
	if err != nil {
		return types.DiscoverRequest{}, err
	}
	req.Name = name
	return req, nil
}

func discoverPeer(ctx context.Context, relayURL string, req types.DiscoverRequest, rootCAPEM []byte) (types.DiscoverResponse, error) {
	relayURL, err := utils.NormalizeRelayURL(relayURL)
	if err != nil {
		return types.DiscoverResponse{}, err
	}

	baseURL, err := url.Parse(relayURL)
	if err != nil {
		return types.DiscoverResponse{}, fmt.Errorf("parse relay url: %w", err)
	}

	rootCAs, err := keyless.RelayRootCAs(ctx, relayURL, baseURL.Hostname(), rootCAPEM)
	if err != nil {
		return types.DiscoverResponse{}, err
	}

	ref, _ := url.Parse(types.PathDiscovery)
	discoverURL := baseURL.ResolveReference(ref)
	query := discoverURL.Query()
	if req.RootHost != "" {
		query.Set("root_host", req.RootHost)
	}
	if req.Name != "" {
		query.Set("name", req.Name)
	}
	discoverURL.RawQuery = query.Encode()

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				ServerName: baseURL.Hostname(),
				RootCAs:    rootCAs,
				NextProtos: []string{"http/1.1"},
			},
			ForceAttemptHTTP2: false,
		},
		Timeout: defaultRequestTimeout,
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, discoverURL.String(), nil)
	if err != nil {
		return types.DiscoverResponse{}, err
	}

	resp, err := httpClient.Do(httpReq)
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
