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
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

const (
	defaultRequestTimeout = 15 * time.Second
	defaultMaxPeers       = 32
	defaultPollInterval   = 30 * time.Second
)

type Resolver interface {
	Discover(context.Context, types.DiscoverRequest) (types.DiscoverResponse, error)
}

type ResolverFunc func(context.Context, types.DiscoverRequest) (types.DiscoverResponse, error)

func (fn ResolverFunc) Discover(ctx context.Context, req types.DiscoverRequest) (types.DiscoverResponse, error) {
	return fn(ctx, req)
}

type Config struct {
	SelfURLs       []string
	Bootstraps     []string
	RootCAPEM      []byte
	RequestTimeout time.Duration
	MaxPeers       int
	OnBootstraps   func([]string)
}

type Service struct {
	resolver       Resolver
	selfURLs       []string
	rootCAPEM      []byte
	requestTimeout time.Duration
	maxPeers       int
	onBootstraps   func([]string)

	mu         sync.RWMutex
	bootstraps []string
}

func New(cfg Config, resolver Resolver) (*Service, error) {
	service := &Service{
		resolver:       resolver,
		rootCAPEM:      append([]byte(nil), cfg.RootCAPEM...),
		requestTimeout: utils.DurationOrDefault(cfg.RequestTimeout, defaultRequestTimeout),
		maxPeers:       utils.IntOrDefault(cfg.MaxPeers, defaultMaxPeers),
		onBootstraps:   cfg.OnBootstraps,
	}
	if err := service.SetSelfURLs(cfg.SelfURLs); err != nil {
		return nil, err
	}
	if err := service.MergeBootstraps(cfg.Bootstraps); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *Service) Bootstraps() []string {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.bootstraps...)
}

func (s *Service) MergeBootstraps(inputs []string) error {
	if s == nil || len(inputs) == 0 {
		return nil
	}

	normalized, err := utils.NormalizeRelayURLs(inputs)
	if err != nil {
		return fmt.Errorf("normalize bootstraps: %w", err)
	}

	s.mu.Lock()
	combined, err := utils.NormalizeRelayURLs(append(append([]string(nil), s.bootstraps...), normalized...))
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("normalize bootstraps: %w", err)
	}
	next := utils.ExcludeURLs(combined, s.selfURLs)
	changed := strings.Join(s.bootstraps, "\x00") != strings.Join(next, "\x00")
	callback := s.onBootstraps
	if changed {
		s.bootstraps = next
	}
	bootstraps := append([]string(nil), s.bootstraps...)
	s.mu.Unlock()

	if changed && callback != nil {
		callback(bootstraps)
	}
	return nil
}

func (s *Service) SetSelfURLs(inputs []string) error {
	if s == nil {
		return nil
	}

	selfURLs, err := utils.NormalizeRelayURLs(inputs)
	if err != nil {
		return fmt.Errorf("normalize self urls: %w", err)
	}

	s.mu.Lock()
	current := strings.Join(s.bootstraps, "\x00")
	s.selfURLs = selfURLs
	s.bootstraps = utils.ExcludeURLs(s.bootstraps, s.selfURLs)
	changed := current != strings.Join(s.bootstraps, "\x00")
	callback := s.onBootstraps
	bootstraps := append([]string(nil), s.bootstraps...)
	s.mu.Unlock()

	if changed && callback != nil {
		callback(bootstraps)
	}
	return nil
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	bootstraps, err := s.responseBootstraps(nil)
	if err != nil {
		utils.WriteAPIError(w, http.StatusInternalServerError, types.APIErrorCodeInternal, err.Error())
		return
	}
	resp := types.DiscoverResponse{
		Found:      false,
		Bootstraps: bootstraps,
	}
	if s == nil || s.resolver == nil {
		utils.WriteAPIData(w, http.StatusOK, resp)
		return
	}

	localResp, err := s.resolver.Discover(r.Context(), req)
	if err != nil {
		utils.WriteAPIError(w, http.StatusInternalServerError, types.APIErrorCodeInternal, err.Error())
		return
	}

	bootstraps, err = s.responseBootstraps(localResp.Bootstraps)
	if err != nil {
		utils.WriteAPIError(w, http.StatusInternalServerError, types.APIErrorCodeInternal, err.Error())
		return
	}
	localResp.Bootstraps = bootstraps
	utils.WriteAPIData(w, http.StatusOK, localResp)
}

func (s *Service) Poll(ctx context.Context, req types.DiscoverRequest) (types.DiscoverResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	req, err := normalizeRequest(req)
	if err != nil {
		return types.DiscoverResponse{}, err
	}

	discovered := s.Bootstraps()
	if len(discovered) == 0 {
		return types.DiscoverResponse{}, errors.New("at least one bootstrap is required")
	}

	queue := append([]string(nil), discovered...)
	seen := make(map[string]struct{}, len(queue))
	var lastErr error
	contacted := false

	for len(queue) > 0 && len(seen) < s.maxPeers {
		bootstrap := queue[0]
		queue = queue[1:]
		if _, ok := seen[bootstrap]; ok {
			continue
		}
		seen[bootstrap] = struct{}{}

		resp, err := discoverPeer(ctx, bootstrap, req, s.rootCAPEM, s.requestTimeout)
		if err != nil {
			lastErr = err
			continue
		}
		contacted = true

		if err := s.MergeBootstraps(resp.Bootstraps); err != nil {
			return types.DiscoverResponse{}, err
		}
		discovered = s.Bootstraps()
		for _, nextBootstrap := range discovered {
			if _, ok := seen[nextBootstrap]; !ok {
				queue = append(queue, nextBootstrap)
			}
		}

		if resp.Found {
			resp.Bootstraps = discovered
			return resp, nil
		}
	}

	if !contacted && lastErr != nil {
		return types.DiscoverResponse{}, lastErr
	}

	return types.DiscoverResponse{
		Found:      false,
		Bootstraps: discovered,
	}, nil
}

func (s *Service) RunPollLoop(ctx context.Context, interval time.Duration, req types.DiscoverRequest) error {
	if s == nil {
		return nil
	}

	interval = utils.DurationOrDefault(interval, defaultPollInterval)
	lastPollErr := ""
	for {
		if len(s.Bootstraps()) > 0 {
			if _, err := s.Poll(ctx, req); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				errText := err.Error()
				if errText != lastPollErr {
					log.Warn().
						Err(err).
						Int("bootstrap_count", len(s.Bootstraps())).
						Str("root_host", req.RootHost).
						Str("name", req.Name).
						Msg("discovery poll failed")
					lastPollErr = errText
				}
			} else if lastPollErr != "" {
				log.Info().
					Int("bootstrap_count", len(s.Bootstraps())).
					Str("root_host", req.RootHost).
					Str("name", req.Name).
					Msg("discovery poll recovered")
				lastPollErr = ""
			}
		}
		if !utils.SleepOrDone(ctx, interval) {
			return nil
		}
	}
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

func (s *Service) responseBootstraps(extra []string) ([]string, error) {
	base := []string(nil)
	if s != nil {
		s.mu.RLock()
		base = append(base, s.selfURLs...)
		base = append(base, s.bootstraps...)
		s.mu.RUnlock()
	}
	bootstraps, err := utils.NormalizeRelayURLs(append(base, extra...))
	if err != nil {
		return nil, fmt.Errorf("normalize bootstraps: %w", err)
	}
	return bootstraps, nil
}

func discoverPeer(ctx context.Context, bootstrap string, req types.DiscoverRequest, rootCAPEM []byte, requestTimeout time.Duration) (types.DiscoverResponse, error) {
	baseURL, err := url.Parse(bootstrap)
	if err != nil {
		return types.DiscoverResponse{}, fmt.Errorf("parse bootstrap url: %w", err)
	}

	rootCAs, err := keyless.RelayRootCAs(ctx, bootstrap, baseURL.Hostname(), rootCAPEM)
	if err != nil {
		return types.DiscoverResponse{}, err
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				ServerName: baseURL.Hostname(),
				RootCAs:    rootCAs,
			},
			ForceAttemptHTTP2: false,
		},
		Timeout: utils.DurationOrDefault(requestTimeout, defaultRequestTimeout),
	}
	defer httpClient.CloseIdleConnections()

	query := url.Values{}
	if req.RootHost != "" {
		query.Set("root_host", req.RootHost)
	}
	if req.Name != "" {
		query.Set("name", req.Name)
	}

	ref := &url.URL{Path: types.PathDiscovery, RawQuery: query.Encode()}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL.ResolveReference(ref).String(), nil)
	if err != nil {
		return types.DiscoverResponse{}, err
	}

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return types.DiscoverResponse{}, err
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return types.DiscoverResponse{}, utils.DecodeAPIRequestError(httpResp)
	}

	envelope, err := utils.DecodeAPIEnvelope[json.RawMessage](httpResp.Body)
	if err != nil {
		return types.DiscoverResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if !envelope.OK {
		return types.DiscoverResponse{}, utils.NewAPIRequestError(httpResp.StatusCode, envelope.Error)
	}

	var resp types.DiscoverResponse
	if err := json.Unmarshal(envelope.Data, &resp); err != nil {
		return types.DiscoverResponse{}, err
	}
	return resp, nil
}
