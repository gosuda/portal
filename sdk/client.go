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

type ClientConfig struct {
	RelayURL           string
	RootCAPEM          []byte
	InsecureSkipVerify bool
	DialTimeout        time.Duration
	RequestTimeout     time.Duration
	HandshakeTimeout   time.Duration
	LeaseTTL           time.Duration
	RenewBefore        time.Duration
	ReadyTarget        int
}

type Client struct {
	baseURL          *url.URL
	httpClient       *http.Client
	rawTLSConfig     *tls.Config
	dialTimeout      time.Duration
	handshakeTimeout time.Duration
	leaseTTL         time.Duration
	renewBefore      time.Duration
	readyTarget      int
}

func NewClient(cfg ClientConfig) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimSpace(cfg.RelayURL))
	if err != nil {
		return nil, fmt.Errorf("parse relay url: %w", err)
	}
	if !strings.EqualFold(baseURL.Scheme, "https") {
		return nil, fmt.Errorf("relay url must use https: %q", cfg.RelayURL)
	}
	if baseURL.Host == "" {
		return nil, fmt.Errorf("relay url host is empty: %q", cfg.RelayURL)
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")
	baseURL.RawQuery = ""
	baseURL.Fragment = ""

	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 15 * time.Second
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 15 * time.Second
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = 2 * time.Minute
	}
	if cfg.RenewBefore <= 0 {
		cfg.RenewBefore = 30 * time.Second
	}
	if cfg.ReadyTarget <= 0 {
		cfg.ReadyTarget = 1
	}

	if len(cfg.RootCAPEM) == 0 && !cfg.InsecureSkipVerify && isLocalRelayHost(baseURL.Hostname()) {
		bootstrapCtx, cancel := context.WithTimeout(context.Background(), cfg.DialTimeout+cfg.HandshakeTimeout)
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
		MinVersion:         tls.VersionTLS12,
		ServerName:         baseURL.Hostname(),
		RootCAs:            rootCAs,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
		NextProtos:         []string{"http/1.1"},
	}

	transport := &http.Transport{
		TLSClientConfig:   baseTLS.Clone(),
		ForceAttemptHTTP2: false,
	}

	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   cfg.RequestTimeout,
		},
		rawTLSConfig:     baseTLS,
		dialTimeout:      cfg.DialTimeout,
		handshakeTimeout: cfg.HandshakeTimeout,
		leaseTTL:         cfg.LeaseTTL,
		renewBefore:      cfg.RenewBefore,
		readyTarget:      cfg.ReadyTarget,
	}, nil
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

	registerReq := types.RegisterRequest{
		Name:         req.Name,
		Hostnames:    append([]string(nil), req.Hostnames...),
		Metadata:     req.Metadata,
		ReverseToken: reverseToken,
		TLS:          true,
		TTLSeconds:   int(leaseTTL / time.Second),
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
	l := &Listener{
		client:       c,
		baseContext:  func() context.Context { return listenerCtx },
		ctxDone:      listenerCtx.Done(),
		cancel:       cancel,
		leaseID:      registerResp.LeaseID,
		hostnames:    append([]string(nil), registerResp.Hostnames...),
		metadata:     registerResp.Metadata,
		reverseToken: reverseToken,
		leaseTTL:     leaseTTL,
		readyTarget:  readyTarget,
		tlsConfig:    tlsConf,
		tlsCloser:    tlsCloser,
		accepted:     make(chan net.Conn, acceptedCap),
		signal:       make(chan struct{}, 1),
	}

	go l.runSupervisor()
	go l.runRenewLoop()
	l.notify()
	return l, nil
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

	req, err := http.NewRequestWithContext(ctx, method, c.resolve(path), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var envelope apiEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !envelope.OK {
		if envelope.Error == nil {
			return fmt.Errorf("api request failed with status %d", resp.StatusCode)
		}
		return fmt.Errorf("%s: %s", envelope.Error.Code, envelope.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
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

	connectURL, err := url.Parse(c.resolve(types.PathSDKConnect))
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		_ = conn.Close()
		return nil, fmt.Errorf("reverse connect failed: %s", strings.TrimSpace(string(body)))
	}

	return wrapBufferedConn(conn, reader), nil
}

func (c *Client) resolve(path string) string {
	ref, _ := url.Parse(path)
	return c.baseURL.ResolveReference(ref).String()
}

type apiEnvelope struct {
	Error *types.APIError `json:"error"`
	Data  json.RawMessage `json:"data"`
	OK    bool            `json:"ok"`
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
