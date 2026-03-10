package sdk

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/types"
)

const (
	defaultDialTimeout         = 5 * time.Second
	defaultRequestTimeout      = 15 * time.Second
	defaultHandshakeTimeout    = 15 * time.Second
	defaultLeaseTTL            = 30 * time.Second
	defaultRenewBefore         = 30 * time.Second
	defaultReadyTarget         = 1
	defaultHTTPShutdownTimeout = 5 * time.Second
)

type relayClient struct {
	baseURL            *url.URL
	httpClient         *http.Client
	rawTLSConfig       *tls.Config
	dialTimeout        time.Duration
	name               string
	requestedHostnames []string
	reverseToken       string
	metadata           types.LeaseMetadata
}

func newRelayClient(relayURL string, cfg ListenerConfig) (*relayClient, error) {
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		return nil, errors.New("listener name is required")
	}

	reverseToken := strings.TrimSpace(cfg.ReverseToken)
	if reverseToken == "" {
		reverseToken = randomToken()
	}

	baseURL, err := url.Parse(strings.TrimSpace(relayURL))
	if err != nil {
		return nil, fmt.Errorf("parse relay url: %w", err)
	}
	if !strings.EqualFold(baseURL.Scheme, "https") {
		return nil, fmt.Errorf("relay url must use https: %q", relayURL)
	}
	if baseURL.Host == "" {
		return nil, fmt.Errorf("relay url host is empty: %q", relayURL)
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	baseURL.RawQuery = ""
	baseURL.Fragment = ""

	rootCAPEM := append([]byte(nil), cfg.RootCAPEM...)
	if len(rootCAPEM) == 0 && isLocalRelayHost(baseURL.Hostname()) {
		bootstrapCtx, cancel := context.WithTimeout(context.Background(), defaultDialTimeout+defaultHandshakeTimeout)
		defer cancel()

		_, resolvedCAPEM, bootstrapErr := keyless.ResolveMaterials(bootstrapCtx, baseURL.String(), baseURL.Hostname())
		if bootstrapErr != nil {
			return nil, fmt.Errorf("bootstrap localhost relay trust: %w", bootstrapErr)
		}
		rootCAPEM = resolvedCAPEM
	}

	rootCAs, err := buildRootCAs(rootCAPEM)
	if err != nil {
		return nil, err
	}

	baseTLS := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: baseURL.Hostname(),
		RootCAs:    rootCAs,
		NextProtos: []string{"http/1.1"},
	}

	transport := &http.Transport{
		TLSClientConfig:   baseTLS.Clone(),
		ForceAttemptHTTP2: false,
	}

	api := &relayClient{
		baseURL:            baseURL,
		httpClient:         &http.Client{Transport: transport, Timeout: defaultRequestTimeout},
		rawTLSConfig:       baseTLS,
		dialTimeout:        defaultDialTimeout,
		name:               name,
		requestedHostnames: append([]string(nil), cfg.Hostnames...),
		reverseToken:       reverseToken,
		metadata:           cloneMetadata(cfg.Metadata),
	}

	checkCtx, cancel := context.WithTimeout(context.Background(), defaultRequestTimeout)
	defer cancel()

	if err := api.ensureCompatible(checkCtx); err != nil {
		api.close()
		return nil, err
	}

	return api, nil
}

func (a *relayClient) close() {
	if a == nil || a.httpClient == nil {
		return
	}
	if transport, ok := a.httpClient.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

func (a *relayClient) registerLease(ctx context.Context, hostnames []string, ttl time.Duration) (types.RegisterResponse, error) {
	if len(hostnames) == 0 {
		hostnames = a.requestedHostnames
	}

	var resp types.RegisterResponse
	if err := a.doJSON(ctx, http.MethodPost, types.PathSDKRegister, types.RegisterRequest{
		Name:         a.name,
		Hostnames:    append([]string(nil), hostnames...),
		Metadata:     cloneMetadata(a.metadata),
		ReverseToken: a.reverseToken,
		TTL:          int(ttl / time.Second),
	}, &resp); err != nil {
		return types.RegisterResponse{}, err
	}
	return resp, nil
}

func (a *relayClient) ensureCompatible(ctx context.Context) error {
	var resp types.DomainResponse
	if err := a.doJSON(ctx, http.MethodGet, types.PathSDKDomain, nil, &resp); err != nil {
		return fmt.Errorf("check relay compatibility: %w", err)
	}
	if strings.TrimSpace(resp.Version) != types.SDKProtocolVersion {
		return fmt.Errorf("relay sdk version mismatch: relay=%q client=%q", strings.TrimSpace(resp.Version), types.SDKProtocolVersion)
	}
	return nil
}

func (a *relayClient) renewLease(ctx context.Context, leaseID string, ttl time.Duration) error {
	return a.doJSON(ctx, http.MethodPost, types.PathSDKRenew, types.RenewRequest{
		LeaseID:      leaseID,
		ReverseToken: a.reverseToken,
		TTL:          int(ttl / time.Second),
	}, &types.RenewResponse{})
}

func (a *relayClient) unregisterLease(ctx context.Context, leaseID string) error {
	return a.doJSON(ctx, http.MethodPost, types.PathSDKUnregister, types.UnregisterRequest{
		LeaseID:      leaseID,
		ReverseToken: a.reverseToken,
	}, nil)
}

func (a *relayClient) openReverseSession(ctx context.Context, leaseID string) (net.Conn, error) {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: a.dialTimeout},
		Config:    a.rawTLSConfig.Clone(),
	}

	conn, err := dialer.DialContext(ctx, "tcp", ensurePort(a.baseURL.Host))
	if err != nil {
		return nil, err
	}

	connectRef, _ := url.Parse(types.PathSDKConnect)
	connectURL := a.baseURL.ResolveReference(connectRef)
	query := connectURL.Query()
	query.Set("lease_id", leaseID)
	connectURL.RawQuery = query.Encode()

	req := &http.Request{
		Method: http.MethodGet,
		URL:    connectURL,
		Host:   a.baseURL.Host,
		Header: make(http.Header),
	}
	req.Header.Set(types.HeaderReverseToken, a.reverseToken)
	req.Header.Set("Connection", "keep-alive")

	if writeErr := req.Write(conn); writeErr != nil {
		_ = conn.Close()
		return nil, writeErr
	}

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		apiErr := decodeAPIResponseError(resp)
		_ = conn.Close()
		return nil, apiErr
	}

	return wrapBufferedConn(conn, reader), nil
}

func (a *relayClient) doJSON(ctx context.Context, method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
		body = bytes.NewReader(buf)
	}

	ref, _ := url.Parse(path)
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL.ResolveReference(ref).String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var envelope types.APIEnvelope[json.RawMessage]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !envelope.OK {
		if envelope.Error == nil {
			return &types.APIRequestError{
				StatusCode: resp.StatusCode,
				Message:    fmt.Sprintf("api request failed with status %d", resp.StatusCode),
			}
		}
		return &types.APIRequestError{
			StatusCode: resp.StatusCode,
			Code:       envelope.Error.Code,
			Message:    envelope.Error.Message,
		}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
}

func buildRootCAs(rootCAPEM []byte) (*x509.CertPool, error) {
	if len(rootCAPEM) == 0 {
		return nil, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(rootCAPEM) {
		return nil, errors.New("failed to parse relay root ca")
	}
	return pool, nil
}

func decodeAPIResponseError(resp *http.Response) error {
	if resp == nil {
		return &types.APIRequestError{Message: "empty api response"}
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	var envelope types.APIEnvelope[json.RawMessage]
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error != nil {
		return &types.APIRequestError{
			StatusCode: resp.StatusCode,
			Code:       envelope.Error.Code,
			Message:    envelope.Error.Message,
		}
	}

	return &types.APIRequestError{
		StatusCode: resp.StatusCode,
		Message:    strings.TrimSpace(string(body)),
	}
}

func randomToken() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return "tok_" + hex.EncodeToString(buf)
}

func ensurePort(host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, "443")
}

func isLocalRelayHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	switch host {
	case "", "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.HasSuffix(host, ".localhost")
}

func cloneMetadata(metadata types.LeaseMetadata) types.LeaseMetadata {
	return types.LeaseMetadata{
		Description: metadata.Description,
		Owner:       metadata.Owner,
		Thumbnail:   metadata.Thumbnail,
		Tags:        append([]string(nil), metadata.Tags...),
		Hide:        metadata.Hide,
	}
}

type bufferedConn struct {
	net.Conn
	reader *bytes.Reader
}

func wrapBufferedConn(conn net.Conn, reader *bufio.Reader) net.Conn {
	if reader == nil || reader.Buffered() == 0 {
		return conn
	}
	buf := make([]byte, reader.Buffered())
	if _, err := io.ReadFull(reader, buf); err != nil {
		return conn
	}
	return &bufferedConn{Conn: conn, reader: bytes.NewReader(buf)}
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if c.reader != nil && c.reader.Len() > 0 {
		return c.reader.Read(p)
	}
	return c.Conn.Read(p)
}
