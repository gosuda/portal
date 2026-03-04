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

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/types"
)

const (
	relayKeepaliveInterval     = 10 * time.Second
	reverseReadTimeout         = 1 * time.Second
	defaultReverseWorkers      = 16
	defaultReverseDialTimeout  = 5 * time.Second
	defaultTLSHandshakeTimeout = 10 * time.Second
)

var fatalReverseConnectRejectionCodes = map[string]struct{}{
	"ip_banned":             {},
	"lease_not_found":       {},
	"method_not_allowed":    {},
	"missing_lease_id":      {},
	"missing_reverse_token": {},
	"tls_required":          {},
	"unauthorized":          {},
	"unsupported_transport": {},
}

type reverseConnectRejectionError struct {
	code       string
	detail     string
	statusCode int
}

func (e *reverseConnectRejectionError) Error() string {
	if e == nil {
		return "reverse connect rejected"
	}
	if e.detail == "" {
		return fmt.Sprintf("reverse connect rejected: status=%d", e.statusCode)
	}
	return fmt.Sprintf("reverse connect rejected: status=%d error=%s", e.statusCode, e.detail)
}

func (e *reverseConnectRejectionError) IsFatal() bool {
	if e == nil {
		return false
	}
	if _, ok := fatalReverseConnectRejectionCodes[e.code]; ok {
		return true
	}
	switch e.statusCode {
	case http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusUpgradeRequired:
		return true
	default:
		return false
	}
}

// Listener is a net.Listener backed by relay tunnel registration.
// The relay connects to this listener after SNI routing resolves the lease.
type Listener struct {
	tlsConfig          *tls.Config
	lease              *portal.Lease
	httpClient         *http.Client
	stopCh             chan struct{}
	acceptCh           chan net.Conn
	relayAddr          string
	closeFns           []func()
	wg                 sync.WaitGroup
	reverseWorkers     int
	reverseDialTimeout time.Duration
	mu                 sync.RWMutex
	closeOnce          sync.Once
	closed             bool
}

var _ net.Listener = (*Listener)(nil)

// NewListener creates a relay-backed listener.
// If tlsConfig is provided, reverse workers complete TLS handshakes before enqueueing connections.
func NewListener(relayAddr string, lease *portal.Lease, tlsConfig *tls.Config, reverseWorkers int, reverseDialTimeout time.Duration, closeFns ...func()) (*Listener, error) {
	if lease == nil {
		return nil, errors.New("lease is required")
	}
	if lease.ID == "" {
		return nil, errors.New("lease ID is required")
	}
	if lease.Name == "" {
		return nil, errors.New("lease name is required")
	}
	if lease.ReverseToken == "" {
		return nil, errors.New("lease reverse token is required")
	}
	if tlsConfig == nil {
		return nil, errors.New("tls config is required")
	}

	apiURL, err := types.NormalizeRelayAPIURL(relayAddr)
	if err != nil {
		return nil, err
	}
	host := types.PortalRootHost(apiURL)
	clientTransport := http.DefaultTransport.(*http.Transport).Clone()
	clientTransport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         host,
		InsecureSkipVerify: types.IsLocalhost(host),
	}

	if reverseWorkers <= 0 {
		reverseWorkers = defaultReverseWorkers
	}
	if reverseDialTimeout <= 0 {
		reverseDialTimeout = defaultReverseDialTimeout
	}
	lease.TLS = true

	return &Listener{
		relayAddr: apiURL,
		lease:     lease,
		httpClient: &http.Client{
			Timeout:   10 * time.Second,
			Transport: clientTransport,
		},
		tlsConfig:          tlsConfig,
		closeFns:           closeFns,
		stopCh:             make(chan struct{}),
		acceptCh:           make(chan net.Conn, 128),
		reverseWorkers:     reverseWorkers,
		reverseDialTimeout: reverseDialTimeout,
	}, nil
}

// Start registers the lease with relay and starts reverse workers.
func (l *Listener) Start() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return net.ErrClosed
	}
	l.mu.Unlock()

	if err := l.registerWithRelay(); err != nil {
		return fmt.Errorf("register lease with relay: %w", err)
	}

	l.wg.Add(1)
	go l.keepaliveLoop()
	for i := range l.reverseWorkers {
		l.wg.Add(1)
		go l.reverseAcceptWorker(i)
	}

	log.Info().
		Str("lease_id", l.lease.ID).
		Str("name", l.lease.Name).
		Str("relay", l.relayAddr).
		Int("reverse_workers", l.reverseWorkers).
		Msg("[SDK] Relay listener started")

	return nil
}

// Accept waits for the next connection from relay.
// Reverse workers deliver ready connections to acceptCh.
func (l *Listener) Accept() (net.Conn, error) {
	l.mu.RLock()
	closed := l.closed
	l.mu.RUnlock()
	if closed {
		return nil, net.ErrClosed
	}

	var conn net.Conn
	select {
	case <-l.stopCh:
		return nil, net.ErrClosed
	case conn = <-l.acceptCh:
		if conn == nil {
			return nil, net.ErrClosed
		}
	}
	return conn, nil
}

// Close unregisters lease from relay.
func (l *Listener) Close() error {
	var retErr error
	l.closeOnce.Do(func() {
		close(l.stopCh)

		l.mu.Lock()
		l.closed = true
		l.mu.Unlock()

		l.wg.Wait()
		for _, closeFn := range l.closeFns {
			if closeFn != nil {
				closeFn()
			}
		}

		if err := l.unregisterFromRelay(); err != nil {
			log.Warn().Err(err).Str("lease_id", l.lease.ID).Msg("[SDK] Failed to unregister lease")
			retErr = err
		}
	})

	return retErr
}

// Addr returns a dummy address (connections come via reverse tunnel).
func (l *Listener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 0}
}

// LeaseID returns lease ID registered to relay.
func (l *Listener) LeaseID() string {
	return l.lease.ID
}

func (l *Listener) keepaliveLoop() {
	defer l.wg.Done()

	ticker := time.NewTicker(relayKeepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			if err := l.sendKeepalive(); err != nil {
				if isLeaseNotFoundError(err) {
					if rerr := l.registerWithRelay(); rerr != nil {
						log.Warn().
							Err(rerr).
							Str("lease_id", l.lease.ID).
							Msg("[SDK] Relay keepalive failed and re-register failed")
					} else {
						log.Info().
							Str("lease_id", l.lease.ID).
							Str("name", l.lease.Name).
							Msg("[SDK] Lease re-registered after relay reset")
					}
					continue
				}
				log.Warn().Err(err).Str("lease_id", l.lease.ID).Msg("[SDK] Relay keepalive failed")
			}
		}
	}
}

func (l *Listener) reverseAcceptWorker(workerID int) {
	defer l.wg.Done()

	for {
		select {
		case <-l.stopCh:
			return
		default:
		}

		conn, err := l.openReverseConnection()
		if err != nil {
			var rejectionErr *reverseConnectRejectionError
			if errors.As(err, &rejectionErr) && rejectionErr.IsFatal() {
				event := log.Error().
					Err(err).
					Str("lease_id", l.lease.ID).
					Int("worker_id", workerID).
					Int("status_code", rejectionErr.statusCode)
				if rejectionErr.code != "" {
					event = event.Str("relay_error_code", rejectionErr.code)
				}
				event.Msg("[SDK] Fatal reverse connect rejection; stopping worker")
				return
			}
			select {
			case <-l.stopCh:
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}

		err = l.waitForReverseStart(conn, portal.TLSStartMarker)
		if err != nil {
			if closeErr := conn.Close(); closeErr != nil {
				log.Debug().Err(closeErr).Msg("[SDK] failed to close reverse connection")
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if errors.Is(err, io.EOF) {
				continue
			}
			log.Debug().
				Err(err).
				Str("lease_id", l.lease.ID).
				Int("worker_id", workerID).
				Msg("[SDK] Reverse wait failed")
			continue
		}

		conn, err = l.prepareAcceptedConnection(conn)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if errors.Is(err, io.EOF) {
				continue
			}
			log.Debug().
				Err(err).
				Str("lease_id", l.lease.ID).
				Int("worker_id", workerID).
				Msg("[SDK] Reverse connection preparation failed")
			continue
		}

		select {
		case <-l.stopCh:
			if closeErr := conn.Close(); closeErr != nil {
				log.Debug().Err(closeErr).Msg("[SDK] failed to close reverse connection on shutdown")
			}
			return
		case l.acceptCh <- conn:
		}
	}
}

func (l *Listener) openReverseConnection() (net.Conn, error) {
	if l.isStopping() {
		return nil, net.ErrClosed
	}
	connectURL, err := relayConnectURL(l.relayAddr, l.lease.ID, l.lease.ReverseToken)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(connectURL)
	if err != nil {
		return nil, fmt.Errorf("parse reverse connect URL: %w", err)
	}
	address := u.Host
	if address == "" {
		return nil, errors.New("reverse connect URL missing host")
	}
	if u.Scheme != "https" {
		return nil, errors.New("reverse connect must use https scheme")
	}
	if _, _, splitErr := net.SplitHostPort(address); splitErr != nil {
		address = net.JoinHostPort(address, "443")
	}

	timeout := l.reverseSetupTimeout()
	ctx, cancel := l.newStopAwareContext(timeout)
	defer cancel()
	dialer := &net.Dialer{
		Timeout: timeout,
	}
	rawConn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		if l.isStopping() || errors.Is(err, context.Canceled) {
			return nil, net.ErrClosed
		}
		return nil, fmt.Errorf("dial reverse tcp: %w", err)
	}
	stopConnWatch := l.closeConnOnStop(rawConn)
	defer stopConnWatch()

	serverName := u.Hostname()
	if serverName == "" {
		_ = rawConn.Close()
		return nil, errors.New("reverse connect URL missing TLS server name")
	}
	tlsConn := tls.Client(rawConn, &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         serverName,
		InsecureSkipVerify: types.IsLocalhost(serverName),
	})
	err = tlsConn.HandshakeContext(ctx)
	if err != nil {
		_ = rawConn.Close()
		if l.isStopping() || errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
			return nil, net.ErrClosed
		}
		return nil, fmt.Errorf("reverse TLS handshake: %w", err)
	}
	conn := net.Conn(tlsConn)

	err = l.writeReverseConnectRequest(conn, u)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader, err := l.readReverseConnectResponse(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if l.isStopping() {
		_ = conn.Close()
		return nil, net.ErrClosed
	}

	return &bufferedConn{Conn: conn, reader: reader}, nil
}

func (l *Listener) prepareAcceptedConnection(conn net.Conn) (net.Conn, error) {
	l.mu.RLock()
	tlsConfig := l.tlsConfig
	l.mu.RUnlock()
	if tlsConfig == nil {
		return conn, nil
	}

	tlsConn := tls.Server(conn, tlsConfig)
	handshakeCtx, cancel := l.newStopAwareContext(defaultTLSHandshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		_ = conn.Close()
		if l.isStopping() || errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
			return nil, net.ErrClosed
		}
		return nil, fmt.Errorf("TLS handshake failed: %w", err)
	}
	return tlsConn, nil
}

func buildReverseConnectRequest(u *url.URL, reverseToken string) (*http.Request, error) {
	if u == nil {
		return nil, errors.New("reverse connect URL is required")
	}
	if u.Host == "" {
		return nil, errors.New("reverse connect URL missing host")
	}

	token := strings.TrimSpace(reverseToken)
	if token == "" {
		return nil, errors.New("reverse token is required")
	}

	requestPath := u.EscapedPath()
	if requestPath == "" {
		requestPath = "/"
	}

	requestURL := &url.URL{
		Path:     requestPath,
		RawPath:  u.RawPath,
		RawQuery: u.RawQuery,
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build reverse connect request: %w", err)
	}
	req.Host = u.Host
	req.Header.Set(portal.ReverseConnectTokenHeader, token)
	req.Header.Set("Connection", "keep-alive")
	return req, nil
}

func (l *Listener) writeReverseConnectRequest(conn net.Conn, u *url.URL) error {
	timeout := l.reverseSetupTimeout()
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("set reverse connect write deadline: %w", err)
	}
	defer func() {
		_ = conn.SetWriteDeadline(time.Time{})
	}()

	req, err := buildReverseConnectRequest(u, l.lease.ReverseToken)
	if err != nil {
		return err
	}
	if err := req.Write(conn); err != nil {
		if l.isStopping() || errors.Is(err, net.ErrClosed) {
			return net.ErrClosed
		}
		return fmt.Errorf("write reverse connect request: %w", err)
	}
	return nil
}

func (l *Listener) readReverseConnectResponse(conn net.Conn) (*bufio.Reader, error) {
	timeout := l.reverseSetupTimeout()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("set reverse connect read deadline: %w", err)
	}
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		if l.isStopping() || errors.Is(err, net.ErrClosed) {
			return nil, net.ErrClosed
		}
		return nil, fmt.Errorf("read reverse connect response: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		code, detail := parseReverseConnectRejection(body)
		if detail == "" {
			detail = strings.TrimSpace(http.StatusText(resp.StatusCode))
		}
		return nil, &reverseConnectRejectionError{
			statusCode: resp.StatusCode,
			code:       code,
			detail:     detail,
		}
	}
	return reader, nil
}

func parseReverseConnectRejection(body []byte) (string, string) {
	trimmedBody := strings.TrimSpace(string(body))
	if trimmedBody == "" {
		return "", ""
	}

	var envelope types.APIRawEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error == nil {
		return "", trimmedBody
	}

	code := strings.TrimSpace(envelope.Error.Code)
	message := strings.TrimSpace(envelope.Error.Message)
	switch {
	case message != "" && code != "":
		return code, fmt.Sprintf("%s (code=%s)", message, code)
	case message != "":
		return code, message
	case code != "":
		return code, code
	default:
		return "", trimmedBody
	}
}

func (l *Listener) reverseSetupTimeout() time.Duration {
	if l.reverseDialTimeout <= 0 {
		return defaultReverseDialTimeout
	}
	return l.reverseDialTimeout
}

func (l *Listener) newStopAwareContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultReverseDialTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	go func() {
		select {
		case <-l.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func (l *Listener) closeConnOnStop(conn net.Conn) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-l.stopCh:
			_ = conn.Close()
		case <-done:
		}
	}()
	return func() {
		close(done)
	}
}

func (l *Listener) isStopping() bool {
	select {
	case <-l.stopCh:
		return true
	default:
		return false
	}
}

func (l *Listener) waitForReverseStart(conn net.Conn, expectedMarker byte) error {
	var marker [1]byte
	for {
		_ = conn.SetReadDeadline(time.Now().Add(reverseReadTimeout))
		_, err := io.ReadFull(conn, marker[:])
		if err == nil {
			_ = conn.SetReadDeadline(time.Time{})
			if marker[0] == portal.ReverseKeepaliveMarker {
				continue
			}
			if marker[0] == expectedMarker {
				return nil
			}
			return fmt.Errorf("invalid reverse marker: %d", marker[0])
		}

		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			select {
			case <-l.stopCh:
				return net.ErrClosed
			default:
				continue
			}
		}

		select {
		case <-l.stopCh:
			return net.ErrClosed
		default:
			return err
		}
	}
}

func (l *Listener) registerWithRelay() error {
	reqBody := types.RegisterRequest{
		LeaseID:      l.lease.ID,
		Name:         l.lease.Name,
		Metadata:     l.lease.Metadata,
		TLS:          l.lease.TLS,
		ReverseToken: l.lease.ReverseToken,
	}

	return l.postJSON(types.PathSDKRegister, reqBody)
}

func (l *Listener) unregisterFromRelay() error {
	reqBody := types.UnregisterRequest{
		LeaseID: l.lease.ID,
	}
	return l.postJSON(types.PathSDKUnregister, reqBody)
}

func (l *Listener) sendKeepalive() error {
	reqBody := types.RenewRequest{
		LeaseID:      l.lease.ID,
		ReverseToken: l.lease.ReverseToken,
	}
	return l.postJSON(types.PathSDKRenew, reqBody)
}

func (l *Listener) postJSON(path string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	endpoint := strings.TrimSuffix(l.relayAddr, "/") + path
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build POST %s request: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := l.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if len(data) == 0 {
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return nil
		}
		return fmt.Errorf("POST %s failed: status=%d", path, resp.StatusCode)
	}

	var envelope types.APIRawEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("POST %s failed: invalid API envelope: %w", path, err)
	}
	if envelope.OK {
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return nil
		}
		return fmt.Errorf("POST %s failed: status=%d body=%s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}

	msg := ""
	if envelope.Error != nil {
		msg = strings.TrimSpace(envelope.Error.Message)
	}
	if msg == "" {
		msg = strings.TrimSpace(string(data))
	}
	if msg == "" {
		msg = fmt.Sprintf("status=%d", resp.StatusCode)
	}
	if envelope.Error != nil && strings.TrimSpace(envelope.Error.Code) != "" {
		return fmt.Errorf("POST %s rejected: %s (code=%s)", path, msg, strings.TrimSpace(envelope.Error.Code))
	}
	return fmt.Errorf("POST %s rejected: %s", path, msg)
}

func isLeaseNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "lease not found")
}

func relayConnectURL(relayAddr, leaseID, token string) (string, error) {
	if strings.TrimSpace(leaseID) == "" {
		return "", errors.New("leaseID is required")
	}
	if strings.TrimSpace(token) == "" {
		return "", errors.New("reverse token is required")
	}

	u, err := url.Parse(relayAddr)
	if err != nil {
		return "", fmt.Errorf("parse relay URL: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("unsupported relay URL scheme: %q (use https)", u.Scheme)
	}
	u.Path = types.PathSDKConnect
	q := u.Query()
	q.Set("lease_id", leaseID)
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

type multiRelayListener struct {
	acceptCh  chan net.Conn
	stopCh    chan struct{}
	leaseID   string
	listeners []net.Listener
	wg        sync.WaitGroup
	closeOnce sync.Once
}

func newMultiRelayListener(leaseID string, listeners []net.Listener) *multiRelayListener {
	m := &multiRelayListener{
		leaseID:   leaseID,
		listeners: listeners,
		acceptCh:  make(chan net.Conn, 128),
		stopCh:    make(chan struct{}),
	}

	for i, listener := range listeners {
		m.wg.Add(1)
		go m.forwardAccept(i, listener)
	}

	return m
}

func (m *multiRelayListener) forwardAccept(index int, listener net.Listener) {
	defer m.wg.Done()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-m.stopCh:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Debug().
				Err(err).
				Int("relay_index", index).
				Msg("[SDK] relay listener accept failed")
			continue
		}

		select {
		case <-m.stopCh:
			_ = conn.Close()
			return
		case m.acceptCh <- conn:
		}
	}
}

func (m *multiRelayListener) Accept() (net.Conn, error) {
	select {
	case <-m.stopCh:
		return nil, net.ErrClosed
	case conn := <-m.acceptCh:
		if conn == nil {
			return nil, net.ErrClosed
		}
		return conn, nil
	}
}

func (m *multiRelayListener) Close() error {
	var retErr error
	m.closeOnce.Do(func() {
		close(m.stopCh)

		for _, listener := range m.listeners {
			if err := listener.Close(); err != nil && retErr == nil {
				retErr = err
			}
		}

		m.wg.Wait()
	})
	return retErr
}

func (m *multiRelayListener) Addr() net.Addr {
	if len(m.listeners) > 0 {
		return m.listeners[0].Addr()
	}
	return &net.TCPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 0}
}

func (m *multiRelayListener) LeaseID() string {
	return m.leaseID
}
