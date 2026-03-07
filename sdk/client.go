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
	defaultLeaseTTL            = 2 * time.Minute
	defaultRenewBefore         = 30 * time.Second
	defaultReadyTarget         = 1
	defaultRetryDelay          = 5 * time.Second
	defaultHTTPShutdownTimeout = 5 * time.Second
)

// ClientConfig configures the SDK client.
type ClientConfig struct {
	RelayURLs []string
	RootCAPEM []byte
}

type Client struct {
	clients []*relayClient
}

type relayClient struct {
	baseURL      *url.URL
	httpClient   *http.Client
	rawTLSConfig *tls.Config
}

func NewClient(cfg ClientConfig) (*Client, error) {
	relayURLs, err := normalizeRelayURLs(cfg.RelayURLs)
	if err != nil {
		return nil, err
	}

	clients := make([]*relayClient, 0, len(relayURLs))
	for _, relayURL := range relayURLs {
		client, err := newRelayClient(cfg, relayURL)
		if err != nil {
			for _, existing := range clients {
				existing.Close()
			}
			return nil, err
		}
		clients = append(clients, client)
	}

	return &Client{clients: clients}, nil
}

func (c *Client) Close() {
	if c == nil {
		return
	}

	for _, client := range c.clients {
		client.Close()
	}
}

func (c *Client) Listen(ctx context.Context, req ListenRequest) (*Listener, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("listener name is required")
	}
	if len(c.clients) == 0 {
		return nil, errors.New("no relay urls configured")
	}

	listener := newListener(ctx)

	entries := make([]*listenerLease, 0, len(c.clients))
	acceptedCap := 0
	for _, client := range c.clients {
		entry, err := client.listenEntry(listener, req)
		if err != nil {
			listener.cancel()
			return nil, errors.Join(err, closeListenerEntries(entries))
		}
		entries = append(entries, entry)
		acceptedCap += entry.readyTarget
	}

	if acceptedCap <= 0 {
		acceptedCap = len(entries)
	}

	listener.accepted = make(chan acceptedConn, acceptedCap)
	listener.entries = entries
	listener.activeCount = len(entries)
	for _, entry := range entries {
		entry.start()
	}

	return listener, nil
}

func newRelayClient(cfg ClientConfig, relayURL string) (*relayClient, error) {
	baseURL, err := url.Parse(relayURL)
	if err != nil {
		return nil, fmt.Errorf("parse relay url: %w", err)
	}

	if len(cfg.RootCAPEM) == 0 && isLocalRelayHost(baseURL.Hostname()) {
		bootstrapCtx, cancel := context.WithTimeout(context.Background(), defaultDialTimeout+defaultHandshakeTimeout)
		defer cancel()

		_, rootCAPEM, bootstrapErr := keyless.ResolveMaterials(bootstrapCtx, baseURL.String(), baseURL.Hostname())
		if bootstrapErr != nil {
			return nil, fmt.Errorf("bootstrap localhost relay trust: %w", bootstrapErr)
		}
		cfg.RootCAPEM = rootCAPEM
	}

	rootCAs, err := buildRootCAs(cfg.RootCAPEM)
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

	return &relayClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   defaultRequestTimeout,
		},
		rawTLSConfig: baseTLS,
	}, nil
}

func normalizeRelayURLs(rawURLs []string) ([]string, error) {
	if len(rawURLs) == 0 {
		return nil, errors.New("relay url is required")
	}

	seen := make(map[string]struct{}, len(rawURLs))
	relayURLs := make([]string, 0, len(rawURLs))
	for _, raw := range rawURLs {
		normalized, err := normalizeRelayURL(raw)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		relayURLs = append(relayURLs, normalized)
	}
	return relayURLs, nil
}

func normalizeRelayURL(raw string) (string, error) {
	baseURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse relay url: %w", err)
	}
	if !strings.EqualFold(baseURL.Scheme, "https") {
		return "", fmt.Errorf("relay url must use https: %q", raw)
	}
	if baseURL.Host == "" {
		return "", fmt.Errorf("relay url host is empty: %q", raw)
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	return baseURL.String(), nil
}

func (c *relayClient) Close() {
	if c == nil || c.httpClient == nil {
		return
	}
	if transport, ok := c.httpClient.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

func (c *relayClient) listenEntry(listener *Listener, req ListenRequest) (*listenerLease, error) {
	reverseToken := strings.TrimSpace(req.ReverseToken)
	if reverseToken == "" {
		reverseToken = randomToken()
	}

	readyTarget := req.ReadyTarget
	if readyTarget <= 0 {
		readyTarget = defaultReadyTarget
	}
	leaseTTL := req.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = defaultLeaseTTL
	}

	registerReq := types.RegisterRequest{
		Name:         req.Name,
		Hostnames:    append([]string(nil), req.Hostnames...),
		Metadata:     req.Metadata,
		ReverseToken: reverseToken,
		TLS:          true,
		TTLSeconds:   int(leaseTTL / time.Second),
	}

	var registerResp types.RegisterResponse
	if err := c.doJSON(listener.ctx, http.MethodPost, types.PathSDKRegister, registerReq, &registerResp); err != nil {
		return nil, err
	}

	tlsConf, tlsCloser, err := keyless.BuildClientTLSConfig(c.baseURL.String(), registerResp.Hostnames)
	if err != nil {
		_ = c.unregisterLease(context.Background(), registerResp.LeaseID, reverseToken)
		return nil, err
	}

	return &listenerLease{
		parent: listener,
		client: c,
		info: ListenerEntry{
			RelayURL:  c.baseURL.String(),
			LeaseID:   registerResp.LeaseID,
			Hostnames: append([]string(nil), registerResp.Hostnames...),
			Metadata:  cloneLeaseMetadata(registerResp.Metadata),
		},
		reverseToken: reverseToken,
		leaseTTL:     leaseTTL,
		readyTarget:  readyTarget,
		tlsConfig:    tlsConf,
		tlsCloser:    tlsCloser,
		active:       true,
	}, nil
}

func (c *relayClient) doJSON(ctx context.Context, method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
		body = bytes.NewReader(buf)
	}

	ref, _ := url.Parse(path)
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL.ResolveReference(ref).String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
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

func (c *relayClient) renewLease(ctx context.Context, leaseID, reverseToken string, ttl time.Duration) error {
	return c.doJSON(ctx, http.MethodPost, types.PathSDKRenew, types.RenewRequest{
		LeaseID:      leaseID,
		ReverseToken: reverseToken,
		TTLSeconds:   int(ttl / time.Second),
	}, &types.RenewResponse{})
}

func (c *relayClient) unregisterLease(ctx context.Context, leaseID, reverseToken string) error {
	return c.doJSON(ctx, http.MethodPost, types.PathSDKUnregister, types.UnregisterRequest{
		LeaseID:      leaseID,
		ReverseToken: reverseToken,
	}, nil)
}

func (c *relayClient) openReverseSession(ctx context.Context, leaseID, reverseToken string) (net.Conn, error) {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: defaultDialTimeout},
		Config:    c.rawTLSConfig.Clone(),
	}

	conn, err := dialer.DialContext(ctx, "tcp", ensurePort(c.baseURL.Host))
	if err != nil {
		return nil, err
	}

	connectRef, _ := url.Parse(types.PathSDKConnect)
	connectURL := c.baseURL.ResolveReference(connectRef)
	query := connectURL.Query()
	query.Set("lease_id", leaseID)
	connectURL.RawQuery = query.Encode()

	req := &http.Request{
		Method: http.MethodGet,
		URL:    connectURL,
		Host:   c.baseURL.Host,
		Header: make(http.Header),
	}
	req.Header.Set(types.HeaderReverseToken, reverseToken)
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

func isLeaseResetError(err error) bool {
	var apiErr *types.APIRequestError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == types.APIErrorCodeLeaseNotFound
}

func isTerminalEntryError(err error) bool {
	var apiErr *types.APIRequestError
	if !errors.As(err, &apiErr) {
		return false
	}

	switch apiErr.Code {
	case types.APIErrorCodeIPBanned, types.APIErrorCodeUnauthorized:
		return true
	default:
		return false
	}
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
