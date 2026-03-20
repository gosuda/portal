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

func DiscoverBootstraps(ctx context.Context, peers []string, req types.DiscoverRequest, rootCAPEM []byte) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	peers, err := utils.NormalizeRelayURLs(peers)
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
		resp, err := discoverPeer(ctx, peer, req, rootCAPEM)
		if err != nil {
			discoverErr = errors.Join(discoverErr, fmt.Errorf("discover %q: %w", peer, err))
			continue
		}

		bootstraps, err = utils.MergeRelayURLs(bootstraps, nil, resp.Bootstraps)
		if err != nil {
			discoverErr = errors.Join(discoverErr, fmt.Errorf("merge %q bootstraps: %w", peer, err))
			continue
		}
		discovered = true
	}

	if !discovered {
		return bootstraps, discoverErr
	}
	return bootstraps, nil
}

func ServeHTTP(w http.ResponseWriter, r *http.Request, selfURLs, bootstraps []string, resolver Resolver) {
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

	resolvedBootstraps, err := buildResponseBootstraps(selfURLs, bootstraps, nil)
	if err != nil {
		utils.WriteAPIError(w, http.StatusInternalServerError, types.APIErrorCodeInternal, err.Error())
		return
	}

	resp := types.DiscoverResponse{
		Found:      false,
		Bootstraps: resolvedBootstraps,
	}
	if resolver == nil {
		utils.WriteAPIData(w, http.StatusOK, resp)
		return
	}

	localResp, err := resolver(r.Context(), req)
	if err != nil {
		utils.WriteAPIError(w, http.StatusInternalServerError, types.APIErrorCodeInternal, err.Error())
		return
	}

	localResp.Bootstraps, err = buildResponseBootstraps(selfURLs, bootstraps, localResp.Bootstraps)
	if err != nil {
		utils.WriteAPIError(w, http.StatusInternalServerError, types.APIErrorCodeInternal, err.Error())
		return
	}
	utils.WriteAPIData(w, http.StatusOK, localResp)
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

func buildResponseBootstraps(selfURLs, bootstraps, extra []string) ([]string, error) {
	merged, err := utils.MergeRelayURLs(bootstraps, selfURLs, extra)
	if err != nil {
		return nil, err
	}
	if len(selfURLs) == 0 {
		return merged, nil
	}

	normalizedSelf, err := utils.NormalizeRelayURLs(selfURLs)
	if err != nil {
		return nil, fmt.Errorf("normalize self urls: %w", err)
	}
	resolvedBootstraps, err := utils.NormalizeRelayURLs(append(normalizedSelf, merged...))
	if err != nil {
		return nil, fmt.Errorf("normalize bootstraps: %w", err)
	}
	return resolvedBootstraps, nil
}
