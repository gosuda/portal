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
	"sync"
	"time"

	"github.com/quic-go/quic-go"

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
	mu               sync.RWMutex
	baseURL          *url.URL
	httpClient       *http.Client
	rawTLSConfig     *tls.Config
	dialTimeout      time.Duration
	requestTimeout   time.Duration
	rootCAPEM        []byte
	name             string
	accessToken      string
	metadata         types.LeaseMetadata
	ownerPrivateKey  string
	ownerAddress     string
	resolvedPublicIP string
}

func newApiClient(relayURL string, cfg ListenerConfig) (*apiClient, error) {
	name, err := utils.NormalizeDNSLabel(cfg.Name)
	if err != nil {
		return nil, err
	}

	identity, err := utils.ResolveSecp256k1Identity(cfg.OwnerPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("resolve owner identity: %w", err)
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

	return &apiClient{
		baseURL:         baseURL,
		dialTimeout:     dialTimeout,
		requestTimeout:  requestTimeout,
		rootCAPEM:       append([]byte(nil), cfg.RootCAPEM...),
		name:            name,
		metadata:        cfg.Metadata.Copy(),
		ownerPrivateKey: identity.PrivateKey,
		ownerAddress:    identity.Address,
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

func (a *apiClient) registerLease(ctx context.Context, ttl time.Duration, udpEnabled bool) (types.RegisterResponse, error) {
	if err := a.ensureHTTPClient(ctx); err != nil {
		return types.RegisterResponse{}, err
	}

	var challenge types.RegisterChallengeResponse
	if err := utils.HTTPDoAPI(ctx, a.httpClient, http.MethodPost, a.baseURL.ResolveReference(&url.URL{Path: types.PathSDKRegisterChallenge}).String(), types.RegisterChallengeRequest{
		Name:         a.name,
		Metadata:     a.metadata.Copy(),
		OwnerAddress: a.ownerAddress,
		TTL:          int(ttl / time.Second),
		UDPEnabled:   udpEnabled,
	}, nil, &challenge); err != nil {
		return types.RegisterResponse{}, err
	}

	signature, err := utils.SignEthereumPersonalMessage(challenge.SIWEMessage, a.ownerPrivateKey)
	if err != nil {
		return types.RegisterResponse{}, err
	}

	var resp types.RegisterResponse
	if err := utils.HTTPDoAPI(ctx, a.httpClient, http.MethodPost, a.baseURL.ResolveReference(&url.URL{Path: types.PathSDKRegister}).String(), types.RegisterRequest{
		ChallengeID:   challenge.ChallengeID,
		SIWEMessage:   challenge.SIWEMessage,
		SIWESignature: signature,
		ReportedIP:    a.reportedIP(ctx),
	}, nil, &resp); err != nil {
		return types.RegisterResponse{}, err
	}
	resp.AccessToken = strings.TrimSpace(resp.AccessToken)
	if resp.AccessToken == "" {
		return types.RegisterResponse{}, errors.New("relay did not return access token")
	}
	a.mu.Lock()
	a.accessToken = resp.AccessToken
	a.mu.Unlock()
	return resp, nil
}

func (a *apiClient) ensureHTTPClient(ctx context.Context) error {
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

func (a *apiClient) reportedIP(ctx context.Context) string {
	if a.resolvedPublicIP == "" {
		a.resolvedPublicIP = utils.ResolvePublicIP(ctx)
	}
	return a.resolvedPublicIP
}

func (a *apiClient) ensureCompatible(ctx context.Context, httpClient *http.Client) error {
	var resp types.DomainResponse
	if err := utils.HTTPDoAPI(ctx, httpClient, http.MethodGet, a.baseURL.ResolveReference(&url.URL{Path: types.PathSDKDomain}).String(), nil, nil, &resp); err != nil {
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
	if strings.TrimSpace(resp.SDKVersion) != types.SDKProtocolVersion {
		return fmt.Errorf("%w: relay sdk version mismatch: relay=%q client=%q", errRelayIncompatible, strings.TrimSpace(resp.SDKVersion), types.SDKProtocolVersion)
	}
	return nil
}

func (a *apiClient) renewLease(ctx context.Context, leaseID string, ttl time.Duration) error {
	if err := a.ensureHTTPClient(ctx); err != nil {
		return err
	}

	a.mu.RLock()
	accessToken := a.accessToken
	a.mu.RUnlock()
	if strings.TrimSpace(accessToken) == "" {
		return errors.New("access token is not available")
	}

	var resp types.RenewResponse
	if err := utils.HTTPDoAPI(ctx, a.httpClient, http.MethodPost, a.baseURL.ResolveReference(&url.URL{Path: types.PathSDKRenew}).String(), types.RenewRequest{
		LeaseID:     leaseID,
		AccessToken: accessToken,
		TTL:         int(ttl / time.Second),
		ReportedIP:  a.reportedIP(ctx),
	}, nil, &resp); err != nil {
		return err
	}
	resp.AccessToken = strings.TrimSpace(resp.AccessToken)
	if resp.AccessToken == "" {
		return errors.New("relay did not return renewed access token")
	}

	a.mu.Lock()
	if a.accessToken == accessToken {
		a.accessToken = resp.AccessToken
	}
	a.mu.Unlock()
	return nil
}

func (a *apiClient) unregisterLease(ctx context.Context, leaseID string) error {
	a.mu.RLock()
	accessToken := a.accessToken
	a.mu.RUnlock()
	return utils.HTTPDoAPI(ctx, a.httpClient, http.MethodPost, a.baseURL.ResolveReference(&url.URL{Path: types.PathSDKUnregister}).String(), types.UnregisterRequest{
		LeaseID:     leaseID,
		AccessToken: accessToken,
	}, nil, nil)
}

func (a *apiClient) openReverseSession(ctx context.Context, leaseID string) (net.Conn, error) {
	if err := a.ensureHTTPClient(ctx); err != nil {
		return nil, err
	}

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
	a.mu.RLock()
	accessToken := a.accessToken
	a.mu.RUnlock()
	req.Header.Set(types.HeaderAccessToken, accessToken)
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
func (a *apiClient) openQUICSession(ctx context.Context, leaseID, accessToken string) (*quic.Conn, error) {
	if err := a.ensureHTTPClient(ctx); err != nil {
		return nil, err
	}

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
		LeaseID:     leaseID,
		AccessToken: accessToken,
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
