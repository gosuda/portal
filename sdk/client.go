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

type ClientOption func(*Client)

func WithRootCAPEM(rootCAPEM []byte) ClientOption {
	rootCAPEM = append([]byte(nil), rootCAPEM...)
	return func(client *Client) {
		client.rootCAPEM = append([]byte(nil), rootCAPEM...)
	}
}

func WithInsecureSkipVerify(skip bool) ClientOption {
	return func(client *Client) {
		client.insecureSkipVerify = skip
	}
}

func WithDialTimeout(timeout time.Duration) ClientOption {
	return func(client *Client) {
		if timeout > 0 {
			client.dialTimeout = timeout
		}
	}
}

func WithRequestTimeout(timeout time.Duration) ClientOption {
	return func(client *Client) {
		if timeout > 0 {
			client.requestTimeout = timeout
		}
	}
}

func WithHandshakeTimeout(timeout time.Duration) ClientOption {
	return func(client *Client) {
		if timeout > 0 {
			client.handshakeTimeout = timeout
		}
	}
}

func WithLeaseTTL(ttl time.Duration) ClientOption {
	return func(client *Client) {
		if ttl > 0 {
			client.leaseTTL = ttl
		}
	}
}

func WithRenewBefore(d time.Duration) ClientOption {
	return func(client *Client) {
		if d > 0 {
			client.renewBefore = d
		}
	}
}

func WithReadyTarget(n int) ClientOption {
	return func(client *Client) {
		if n > 0 {
			client.readyTarget = n
		}
	}
}

type Client struct {
	baseURL            *url.URL
	httpClient         *http.Client
	rawTLSConfig       *tls.Config
	rootCAPEM          []byte
	insecureSkipVerify bool
	dialTimeout        time.Duration
	requestTimeout     time.Duration
	handshakeTimeout   time.Duration
	leaseTTL           time.Duration
	renewBefore        time.Duration
	readyTarget        int
}

func NewClient(relayURL string, options ...ClientOption) (*Client, error) {
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

	client := &Client{
		baseURL:          baseURL,
		dialTimeout:      defaultDialTimeout,
		requestTimeout:   defaultRequestTimeout,
		handshakeTimeout: defaultHandshakeTimeout,
		leaseTTL:         defaultLeaseTTL,
		renewBefore:      defaultRenewBefore,
		readyTarget:      defaultReadyTarget,
	}
	for _, option := range options {
		if option != nil {
			option(client)
		}
	}

	if len(client.rootCAPEM) == 0 && !client.insecureSkipVerify && isLocalRelayHost(baseURL.Hostname()) {
		bootstrapCtx, cancel := context.WithTimeout(context.Background(), defaultDialTimeout+defaultHandshakeTimeout)
		defer cancel()

		_, rootCAPEM, bootstrapErr := keyless.ResolveMaterials(bootstrapCtx, baseURL.String(), baseURL.Hostname())
		if bootstrapErr != nil {
			return nil, fmt.Errorf("bootstrap localhost relay trust: %w", bootstrapErr)
		}
		client.rootCAPEM = rootCAPEM
	}

	rootCAs, err := buildRootCAs(client.rootCAPEM)
	if err != nil {
		return nil, err
	}

	baseTLS := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         baseURL.Hostname(),
		RootCAs:            rootCAs,
		InsecureSkipVerify: client.insecureSkipVerify,
		NextProtos:         []string{"http/1.1"},
	}

	transport := &http.Transport{
		TLSClientConfig:   baseTLS.Clone(),
		ForceAttemptHTTP2: false,
	}

	client.httpClient = &http.Client{
		Transport: transport,
		Timeout:   client.requestTimeout,
	}
	client.rawTLSConfig = baseTLS
	return client, nil
}

func (c *Client) Close() {
	if c == nil || c.httpClient == nil {
		return
	}
	if transport, ok := c.httpClient.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

func (c *Client) Listen(ctx context.Context, req ListenRequest) (*Listener, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("listener name is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	reverseToken := strings.TrimSpace(req.ReverseToken)
	if reverseToken == "" {
		reverseToken = randomToken()
	}

	readyTarget := req.ReadyTarget
	if readyTarget <= 0 {
		readyTarget = c.readyTarget
	}
	leaseTTL := req.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = c.leaseTTL
	}
	acceptedCap := max(readyTarget*2, 1)

	transport := strings.TrimSpace(strings.ToLower(req.Transport))
	if transport == "" {
		transport = types.TransportTCP
	}

	registerReq := types.RegisterRequest{
		Name:         req.Name,
		Hostnames:    req.Hostnames,
		Metadata:     req.Metadata,
		ReverseToken: reverseToken,
		TLS:          true,
		TTLSeconds:   int(leaseTTL / time.Second),
		Transport:    transport,
	}

	var registerResp types.RegisterResponse
	if err := c.doJSON(ctx, http.MethodPost, types.PathSDKRegister, registerReq, &registerResp); err != nil {
		return nil, err
	}

	tlsConf, tlsCloser, err := keyless.BuildClientTLSConfig(c.baseURL.String(), registerResp.Hostnames)
	if err != nil {
		_ = c.unregisterLease(context.Background(), registerResp.LeaseID, reverseToken)
		return nil, err
	}

	listenerCtx, cancel := context.WithCancel(ctx)
	listener := &Listener{
		client:       c,
		baseContext:  func() context.Context { return listenerCtx },
		ctxDone:      listenerCtx.Done(),
		cancel:       cancel,
		name:         strings.TrimSpace(req.Name),
		leaseID:      registerResp.LeaseID,
		hostnames:    registerResp.Hostnames,
		metadata:     registerResp.Metadata,
		reverseToken: reverseToken,
		udpAddr:      registerResp.UDPAddr,
		quicAddr:     registerResp.QUICAddr,
		leaseTTL:     leaseTTL,
		readyTarget:  readyTarget,
		tlsConfig:    tlsConf,
		tlsCloser:    tlsCloser,
		accepted:     make(chan net.Conn, acceptedCap),
		signal:       make(chan struct{}, 1),
	}

	go listener.runSupervisor()
	go listener.runRenewLoop()
	listener.notify()
	return listener, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, payload any, out any) error {
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

func (c *Client) registerLease(ctx context.Context, req types.RegisterRequest) (types.RegisterResponse, error) {
	var resp types.RegisterResponse
	if err := c.doJSON(ctx, http.MethodPost, types.PathSDKRegister, req, &resp); err != nil {
		return types.RegisterResponse{}, err
	}
	return resp, nil
}

func (c *Client) renewLease(ctx context.Context, leaseID, reverseToken string, ttl time.Duration) error {
	return c.doJSON(ctx, http.MethodPost, types.PathSDKRenew, types.RenewRequest{
		LeaseID:      leaseID,
		ReverseToken: reverseToken,
		TTLSeconds:   int(ttl / time.Second),
	}, &types.RenewResponse{})
}

func (c *Client) unregisterLease(ctx context.Context, leaseID, reverseToken string) error {
	return c.doJSON(ctx, http.MethodPost, types.PathSDKUnregister, types.UnregisterRequest{
		LeaseID:      leaseID,
		ReverseToken: reverseToken,
	}, nil)
}

func (c *Client) openReverseSession(ctx context.Context, leaseID, reverseToken string) (net.Conn, error) {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: c.dialTimeout},
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
