package sdk

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/gosuda/portal/v2/portal/discovery"
	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

const (
	defaultDialTimeout         = 5 * time.Second
	defaultRequestTimeout      = 15 * time.Second
	defaultHandshakeTimeout    = 15 * time.Second
	defaultLeaseTTL            = 30 * time.Second
	defaultRenewBefore         = 30 * time.Second
	defaultReadyTarget         = 2
	defaultRetryWait           = 3 * time.Second
	defaultHTTPShutdownTimeout = 5 * time.Second
)

var errRelayIncompatible = errors.New("relay is incompatible")

type apiClient struct {
	baseURL        *url.URL
	httpClient     *http.Client
	rawTLSConfig   *tls.Config
	dialTimeout    time.Duration
	requestTimeout time.Duration
	rootCAPEM      []byte
	name           string
	reverseToken   string
	discovery      bool
	metadata       types.LeaseMetadata
	ownerAddress   string
}

func newApiClient(relayURL string, cfg ListenerConfig) (*apiClient, error) {
	name, err := utils.NormalizeDNSLabel(cfg.Name)
	if err != nil {
		return nil, err
	}

	reverseToken := strings.TrimSpace(cfg.ReverseToken)
	if reverseToken == "" {
		reverseToken = utils.RandomID("tok_")
	}

	normalizedRelayURL, err := utils.NormalizeRelayURL(relayURL)
	if err != nil {
		return nil, err
	}

	baseURL, err := url.Parse(normalizedRelayURL)
	if err != nil {
		return nil, fmt.Errorf("parse relay url: %w", err)
	}

	dialTimeout := utils.DurationOrDefault(cfg.DialTimeout, defaultDialTimeout)
	requestTimeout := utils.DurationOrDefault(cfg.RequestTimeout, defaultRequestTimeout)
	ownerAddress := strings.TrimSpace(cfg.OwnerAddress)
	if ownerAddress != "" {
		ownerAddress, err = discovery.NormalizeEVMAddress(ownerAddress)
		if err != nil {
			return nil, fmt.Errorf("normalize owner address: %w", err)
		}
	}

	return &apiClient{
		baseURL:        baseURL,
		dialTimeout:    dialTimeout,
		requestTimeout: requestTimeout,
		rootCAPEM:      append([]byte(nil), cfg.RootCAPEM...),
		name:           name,
		reverseToken:   reverseToken,
		discovery:      cfg.Discovery,
		metadata:       cfg.Metadata.Copy(),
		ownerAddress:   ownerAddress,
	}, nil
}

func (a *apiClient) close() {
	if a == nil || a.httpClient == nil {
		return
	}
	if transport, ok := a.httpClient.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

func (a *apiClient) registerLease(ctx context.Context, ttl time.Duration, udpEnabled bool, bootstraps []string) (types.RegisterResponse, error) {
	var resp types.RegisterResponse
	if !a.discovery {
		bootstraps = nil
	}
	if err := a.doJSON(ctx, http.MethodPost, types.PathSDKRegister, types.RegisterRequest{
		Name:         a.name,
		Metadata:     a.metadata.Copy(),
		OwnerAddress: a.ownerAddress,
		ReverseToken: a.reverseToken,
		TTL:          int(ttl / time.Second),
		Bootstraps:   bootstraps,
		UDPEnabled:   udpEnabled,
	}, &resp); err != nil {
		return types.RegisterResponse{}, err
	}
	return resp, nil
}

func (a *apiClient) ensureReady(ctx context.Context) error {
	if a.httpClient != nil && a.rawTLSConfig != nil {
		return nil
	}

	bootstrapCtx, cancel := context.WithTimeout(ctx, defaultDialTimeout+defaultHandshakeTimeout)
	defer cancel()

	rootCAs, err := keyless.RelayRootCAs(bootstrapCtx, a.baseURL.String(), a.baseURL.Hostname(), a.rootCAPEM)
	if err != nil {
		return err
	}

	rawTLSConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: a.baseURL.Hostname(),
		RootCAs:    rootCAs,
		NextProtos: []string{"http/1.1"},
	}
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   rawTLSConfig.Clone(),
			ForceAttemptHTTP2: false,
		},
		Timeout: a.requestTimeout,
	}
	if err := a.ensureCompatible(ctx, httpClient); err != nil {
		a.close()
		return err
	}

	a.close()
	a.httpClient = httpClient
	a.rawTLSConfig = rawTLSConfig
	return nil
}

func (a *apiClient) ensureCompatible(ctx context.Context, httpClient *http.Client) error {
	var resp types.DomainResponse
	if err := a.doJSONWithClient(ctx, httpClient, http.MethodGet, types.PathSDKDomain, nil, &resp); err != nil {
		err = fmt.Errorf("check relay compatibility: %w", err)
		var netErr net.Error
		var apiErr *types.APIRequestError
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) {
			return err
		}
		if errors.As(err, &apiErr) && apiErr.StatusCode >= 500 {
			return err
		}
		return fmt.Errorf("%w: %w", errRelayIncompatible, err)
	}
	if strings.TrimSpace(resp.Version) != types.SDKProtocolVersion {
		return fmt.Errorf("%w: relay sdk version mismatch: relay=%q client=%q", errRelayIncompatible, strings.TrimSpace(resp.Version), types.SDKProtocolVersion)
	}
	return nil
}

func (a *apiClient) renewLease(ctx context.Context, leaseID string, ttl time.Duration) error {
	return a.doJSON(ctx, http.MethodPost, types.PathSDKRenew, types.RenewRequest{
		LeaseID:      leaseID,
		ReverseToken: a.reverseToken,
		TTL:          int(ttl / time.Second),
	}, &types.RenewResponse{})
}

func (a *apiClient) unregisterLease(ctx context.Context, leaseID string) error {
	return a.doJSON(ctx, http.MethodPost, types.PathSDKUnregister, types.UnregisterRequest{
		LeaseID:      leaseID,
		ReverseToken: a.reverseToken,
	}, nil)
}

func (a *apiClient) openReverseSession(ctx context.Context, leaseID string) (net.Conn, error) {
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: a.dialTimeout},
		Config:    a.rawTLSConfig.Clone(),
	}

	conn, err := dialer.DialContext(ctx, "tcp", utils.EnsurePort(a.baseURL.Host))
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
		apiErr := utils.DecodeAPIRequestError(resp)
		_ = conn.Close()
		return nil, apiErr
	}

	return wrapBufferedConn(conn, reader), nil
}

func (a *apiClient) doJSON(ctx context.Context, method, path string, payload any, out any) error {
	return a.doJSONWithClient(ctx, a.httpClient, method, path, payload, out)
}

func (a *apiClient) doJSONWithClient(ctx context.Context, httpClient *http.Client, method, path string, payload any, out any) error {
	if httpClient == nil {
		return errors.New("api client is not ready")
	}

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

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return utils.DecodeAPIRequestError(resp)
	}

	envelope, err := utils.DecodeAPIEnvelope[json.RawMessage](resp.Body)
	if err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if !envelope.OK {
		return utils.NewAPIRequestError(resp.StatusCode, envelope.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(envelope.Data, out)
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

// openQUICSession opens a QUIC connection to the relay for datagram transport.
func (a *apiClient) openQUICSession(ctx context.Context, leaseID, reverseToken string) (*quic.Conn, error) {
	tlsConf := a.rawTLSConfig.Clone()
	tlsConf.NextProtos = []string{"portal-tunnel"}

	quicConf := &quic.Config{
		EnableDatagrams: true,
		KeepAlivePeriod: 15 * time.Second,
		MaxIdleTimeout:  60 * time.Second,
	}

	dialAddr := utils.EnsurePort(a.baseURL.Host)
	conn, err := quic.DialAddr(ctx, dialAddr, tlsConf, quicConf)
	if err != nil {
		return nil, fmt.Errorf("quic dial: %w", err)
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		_ = conn.CloseWithError(1, "stream open failed")
		return nil, fmt.Errorf("open control stream: %w", err)
	}

	controlMsg := types.QUICControlMessage{
		LeaseID:      leaseID,
		ReverseToken: reverseToken,
	}
	if err := json.NewEncoder(stream).Encode(controlMsg); err != nil {
		_ = conn.CloseWithError(1, "control write failed")
		return nil, fmt.Errorf("write control: %w", err)
	}

	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	var resp types.QUICControlResponse
	if err := json.NewDecoder(io.LimitReader(stream, 4096)).Decode(&resp); err != nil {
		_ = conn.CloseWithError(1, "control read failed")
		return nil, fmt.Errorf("read control response: %w", err)
	}
	if !resp.OK {
		_ = conn.CloseWithError(1, resp.Error)
		return nil, fmt.Errorf("quic connect rejected: %s", resp.Error)
	}

	return conn, nil
}
