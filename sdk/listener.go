package sdk

import (
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
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/net/websocket"
	"gosuda.org/portal/portal"
)

const (
	relayKeepaliveInterval    = 10 * time.Second
	reverseReadTimeout        = 1 * time.Second
	reverseStartMarker        = byte(0x01)
	defaultReverseWorkers     = 2
	maxReverseWorkers         = 16
	defaultReverseDialTimeout = 5 * time.Second
)

// Listener is a net.Listener backed by relay tunnel registration.
// The relay connects to this listener after SNI routing resolves the lease.
type Listener struct {
	relayAddr string
	lease     *portal.Lease

	httpClient *http.Client

	mu                 sync.RWMutex
	listener           net.Listener
	closed             bool
	acceptCh           chan net.Conn
	reverseWorkers     int
	reverseDialTimeout time.Duration

	// TLS configuration
	tlsConfig   *tls.Config
	autocertMgr *autocert.Manager

	stopCh    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

var _ net.Listener = (*Listener)(nil)

// NewListener creates a relay-backed listener.
// If tlsConfig is provided, the listener will perform TLS handshake on incoming connections.
func NewListener(relayAddr string, lease *portal.Lease, tlsConfig *tls.Config, autocertMgr *autocert.Manager, reverseWorkers int, reverseDialTimeout time.Duration) (*Listener, error) {
	if lease == nil {
		return nil, fmt.Errorf("lease is required")
	}
	if lease.ID == "" {
		return nil, fmt.Errorf("lease ID is required")
	}
	if lease.Name == "" {
		return nil, fmt.Errorf("lease name is required")
	}
	if strings.TrimSpace(lease.ReverseToken) == "" {
		return nil, fmt.Errorf("lease reverse token is required")
	}

	apiURL, err := normalizeRelayAPIURL(relayAddr)
	if err != nil {
		return nil, err
	}

	if reverseWorkers <= 0 {
		reverseWorkers = defaultReverseWorkers
	}
	if reverseWorkers > maxReverseWorkers {
		reverseWorkers = maxReverseWorkers
	}
	if reverseDialTimeout <= 0 {
		reverseDialTimeout = defaultReverseDialTimeout
	}

	return &Listener{
		relayAddr: apiURL,
		lease:     lease,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		tlsConfig:          tlsConfig,
		autocertMgr:        autocertMgr,
		stopCh:             make(chan struct{}),
		acceptCh:           make(chan net.Conn, 128),
		reverseWorkers:     reverseWorkers,
		reverseDialTimeout: reverseDialTimeout,
	}, nil
}

// Start initializes the local listener, then registers it to relay.
func (l *Listener) Start() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return net.ErrClosed
	}
	if l.listener != nil {
		l.mu.Unlock()
		return nil
	}
	l.mu.Unlock()

	rawListener, err := net.Listen("tcp", ":0")
	if err != nil {
		return fmt.Errorf("listen local tunnel socket: %w", err)
	}

	addr, ok := rawListener.Addr().(*net.TCPAddr)
	if !ok {
		rawListener.Close()
		return fmt.Errorf("unexpected listener address type: %T", rawListener.Addr())
	}

	// Use localhost for local development. For production/Docker, this should be configurable.
	tunnelAddr := fmt.Sprintf("127.0.0.1:%d", addr.Port)
	if err := l.registerWithRelay(tunnelAddr); err != nil {
		rawListener.Close()
		return fmt.Errorf("register lease with relay: %w", err)
	}

	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		rawListener.Close()
		return net.ErrClosed
	}
	l.lease.Address = tunnelAddr
	l.listener = rawListener
	l.mu.Unlock()

	l.wg.Add(1)
	go l.keepaliveLoop()
	l.wg.Add(1)
	go l.localAcceptLoop(rawListener)
	for i := 0; i < l.reverseWorkers; i++ {
		l.wg.Add(1)
		go l.reverseAcceptWorker(i)
	}

	log.Info().
		Str("lease_id", l.lease.ID).
		Str("name", l.lease.Name).
		Str("relay", l.relayAddr).
		Str("tunnel_addr", tunnelAddr).
		Int("reverse_workers", l.reverseWorkers).
		Msg("[SDK] Relay listener started")

	return nil
}

// Accept waits for the next connection from relay.
// If TLS is enabled, it performs TLS handshake before returning the connection.
func (l *Listener) Accept() (net.Conn, error) {
	l.mu.RLock()
	closed := l.closed
	tlsConfig := l.tlsConfig
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

	// If TLS is enabled, wrap the connection and perform handshake
	if tlsConfig != nil {
		tlsConn := tls.Server(conn, tlsConfig)
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("TLS handshake failed: %w", err)
		}
		return tlsConn, nil
	}

	return conn, nil
}

// Close unregisters lease from relay and closes local listener.
func (l *Listener) Close() error {
	var retErr error
	l.closeOnce.Do(func() {
		close(l.stopCh)

		l.mu.Lock()
		l.closed = true
		listener := l.listener
		l.listener = nil
		l.mu.Unlock()

		if listener != nil {
			retErr = listener.Close()
		}

		l.wg.Wait()

		if err := l.unregisterFromRelay(); err != nil {
			log.Warn().Err(err).Str("lease_id", l.lease.ID).Msg("[SDK] Failed to unregister lease")
			if retErr == nil {
				retErr = err
			}
		}
	})

	return retErr
}

// Addr returns local listener address.
func (l *Listener) Addr() net.Addr {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.listener != nil {
		return l.listener.Addr()
	}
	return nil
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
					if rerr := l.reRegisterLease(); rerr != nil {
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

func (l *Listener) localAcceptLoop(listener net.Listener) {
	defer l.wg.Done()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-l.stopCh:
				return
			default:
				log.Warn().Err(err).Str("lease_id", l.lease.ID).Msg("[SDK] Local accept error")
				continue
			}
		}

		select {
		case <-l.stopCh:
			conn.Close()
			return
		case l.acceptCh <- conn:
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
			select {
			case <-l.stopCh:
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}

		if err := l.waitForReverseStart(conn); err != nil {
			conn.Close()
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Debug().
				Err(err).
				Str("lease_id", l.lease.ID).
				Int("worker_id", workerID).
				Msg("[SDK] Reverse wait failed")
			continue
		}

		select {
		case <-l.stopCh:
			conn.Close()
			return
		case l.acceptCh <- conn:
		}
	}
}

func (l *Listener) openReverseConnection() (net.Conn, error) {
	connectURL, err := relayConnectURL(l.relayAddr, l.lease.ID, l.lease.ReverseToken)
	if err != nil {
		return nil, err
	}

	cfg, err := websocket.NewConfig(connectURL, l.relayAddr)
	if err != nil {
		return nil, fmt.Errorf("new reverse websocket config: %w", err)
	}
	cfg.Dialer = &net.Dialer{
		Timeout: l.reverseDialTimeout,
	}
	ctx, cancel := context.WithTimeout(context.Background(), l.reverseDialTimeout)
	defer cancel()

	conn, err := cfg.DialContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("dial reverse websocket: %w", err)
	}
	conn.PayloadType = websocket.BinaryFrame
	return conn, nil
}

func (l *Listener) waitForReverseStart(conn net.Conn) error {
	var marker [1]byte
	for {
		_ = conn.SetReadDeadline(time.Now().Add(reverseReadTimeout))
		_, err := io.ReadFull(conn, marker[:])
		if err == nil {
			_ = conn.SetReadDeadline(time.Time{})
			if marker[0] != reverseStartMarker {
				return fmt.Errorf("invalid reverse marker: %d", marker[0])
			}
			return nil
		}

		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
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

func (l *Listener) registerWithRelay(tunnelAddr string) error {
	reqBody := RegisterRequest{
		LeaseID:      l.lease.ID,
		Name:         l.lease.Name,
		Address:      tunnelAddr,
		Metadata:     l.lease.Metadata,
		TLSEnabled:   l.lease.TLSEnabled,
		ReverseToken: l.lease.ReverseToken,
	}

	return l.postJSON("/api/register", reqBody)
}

func (l *Listener) unregisterFromRelay() error {
	reqBody := UnregisterRequest{
		LeaseID: l.lease.ID,
	}
	return l.postJSON("/api/unregister", reqBody)
}

func (l *Listener) sendKeepalive() error {
	reqBody := RenewRequest{
		LeaseID:      l.lease.ID,
		ReverseToken: l.lease.ReverseToken,
	}
	return l.postJSON("/api/renew", reqBody)
}

func (l *Listener) reRegisterLease() error {
	l.mu.RLock()
	addr := strings.TrimSpace(l.lease.Address)
	l.mu.RUnlock()
	if addr == "" {
		return fmt.Errorf("lease address is empty; cannot re-register")
	}
	return l.registerWithRelay(addr)
}

func (l *Listener) postJSON(path string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	endpoint := strings.TrimSuffix(l.relayAddr, "/") + path
	resp, err := l.httpClient.Post(endpoint, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s failed: status=%d body=%s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}

	data, _ := io.ReadAll(resp.Body)
	if len(data) == 0 {
		return nil
	}

	var apiResp struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &apiResp); err != nil {
		// Non-JSON success payloads are treated as successful.
		return nil
	}
	if apiResp.Success != nil && !*apiResp.Success {
		msg := strings.TrimSpace(apiResp.Message)
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("POST %s rejected: %s", path, msg)
	}

	return nil
}

func isLeaseNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "lease not found")
}

func normalizeRelayAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty relay URL")
	}

	// Accept host:port input.
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse relay URL: %w", err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("relay URL missing host: %q", raw)
	}

	if host := strings.ToLower(strings.TrimSpace(u.Hostname())); strings.HasSuffix(host, ".localhost") {
		port := u.Port()
		if port != "" {
			u.Host = net.JoinHostPort("localhost", port)
		} else {
			u.Host = "localhost"
		}
	}

	switch u.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("unsupported relay URL scheme: %q (use http/https)", u.Scheme)
	}

	if p := strings.TrimSpace(u.Path); p != "" && p != "/" {
		return "", fmt.Errorf("relay URL must not include path: %q", raw)
	}

	u.RawQuery = ""
	u.Fragment = ""
	u.Path = ""

	return strings.TrimSuffix(u.String(), "/"), nil
}

func relayConnectURL(relayAddr, leaseID, token string) (string, error) {
	if strings.TrimSpace(leaseID) == "" {
		return "", fmt.Errorf("leaseID is required")
	}
	if strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("reverse token is required")
	}

	u, err := url.Parse(relayAddr)
	if err != nil {
		return "", fmt.Errorf("parse relay URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported relay URL scheme: %q", u.Scheme)
	}
	u.Path = "/api/connect"
	q := u.Query()
	q.Set("lease_id", leaseID)
	q.Set("token", token)
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}
