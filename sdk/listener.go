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
	"golang.org/x/net/websocket"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/types"
)

const (
	relayKeepaliveInterval    = 10 * time.Second
	reverseReadTimeout        = 1 * time.Second
	defaultReverseWorkers     = 16
	defaultReverseDialTimeout = 5 * time.Second
)

// Listener is a net.Listener backed by relay tunnel registration.
// The relay connects to this listener after SNI routing resolves the lease.
type Listener struct {
	relayAddr string
	lease     *portal.Lease

	httpClient *http.Client

	mu                 sync.RWMutex
	closed             bool
	acceptCh           chan net.Conn
	reverseWorkers     int
	reverseDialTimeout time.Duration

	// TLS configuration
	tlsConfig *tls.Config
	closeFns  []func()

	stopCh    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

var _ net.Listener = (*Listener)(nil)

// NewListener creates a relay-backed listener.
// If tlsConfig is provided, the listener will perform TLS handshake on incoming connections.
func NewListener(relayAddr string, lease *portal.Lease, tlsConfig *tls.Config, reverseWorkers int, reverseDialTimeout time.Duration, closeFns ...func()) (*Listener, error) {
	if lease == nil {
		return nil, fmt.Errorf("lease is required")
	}
	if lease.ID == "" {
		return nil, fmt.Errorf("lease ID is required")
	}
	if lease.Name == "" {
		return nil, fmt.Errorf("lease name is required")
	}
	if lease.ReverseToken == "" {
		return nil, fmt.Errorf("lease reverse token is required")
	}

	apiURL, err := types.NormalizeRelayAPIURL(relayAddr)
	if err != nil {
		return nil, err
	}

	if reverseWorkers <= 0 {
		reverseWorkers = defaultReverseWorkers
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
	for i := 0; i < l.reverseWorkers; i++ {
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
		if err := tlsConn.HandshakeContext(context.Background()); err != nil {
			if closeErr := conn.Close(); closeErr != nil {
				log.Debug().Err(closeErr).Msg("[SDK] failed to close TLS connection after handshake error")
			}
			return nil, fmt.Errorf("TLS handshake failed: %w", err)
		}
		return tlsConn, nil
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
			select {
			case <-l.stopCh:
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}

		expectedMarker := portal.HTTPStartMarker
		if l.tlsConfig != nil {
			expectedMarker = portal.TLSStartMarker
		}

		if err := l.waitForReverseStart(conn, expectedMarker); err != nil {
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
	connectURL, err := relayConnectURL(l.relayAddr, l.lease.ID, l.lease.ReverseToken)
	if err != nil {
		return nil, err
	}

	cfg, err := websocket.NewConfig(connectURL, l.relayAddr)
	if err != nil {
		return nil, fmt.Errorf("new reverse websocket config: %w", err)
	}
	cfg.Header.Set(portal.ReverseConnectTokenHeader, l.lease.ReverseToken)
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

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s failed: status=%d body=%s", path, resp.StatusCode, strings.TrimSpace(string(data)))
	}

	data, _ := io.ReadAll(resp.Body)
	if len(data) == 0 {
		return nil
	}

	var apiResp types.APIResponse
	if err := json.Unmarshal(data, &apiResp); err != nil {
		// Non-JSON success payloads are treated as successful.
		return nil
	}
	if !apiResp.Success {
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
	u.Path = types.PathSDKConnect
	q := u.Query()
	q.Set("lease_id", leaseID)
	u.RawQuery = q.Encode()
	u.Fragment = ""
	return u.String(), nil
}

type multiRelayListener struct {
	leaseID   string
	listeners []net.Listener

	acceptCh chan net.Conn
	stopCh   chan struct{}

	closeOnce sync.Once
	wg        sync.WaitGroup
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
