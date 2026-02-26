package sdk

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal/api"
)

// Funnel SDK errors.
var (
	ErrFunnelClosed     = errors.New("funnel listener closed")
	ErrRegistrationFail = errors.New("funnel registration failed")
)

// FunnelOption configures a FunnelClient.
type FunnelOption func(*funnelConfig)

type funnelConfig struct {
	Description string
	Tags        []string
	Thumbnail   string
	Owner       string
	Hide        bool
	Workers     int
}

// WithFunnelDescription sets the metadata description.
func WithFunnelDescription(desc string) FunnelOption {
	return func(c *funnelConfig) { c.Description = desc }
}

// WithFunnelTags sets the metadata tags.
func WithFunnelTags(tags ...string) FunnelOption {
	return func(c *funnelConfig) { c.Tags = tags }
}

// WithFunnelThumbnail sets the metadata thumbnail URL.
func WithFunnelThumbnail(url string) FunnelOption {
	return func(c *funnelConfig) { c.Thumbnail = url }
}

// WithFunnelOwner sets the metadata owner name.
func WithFunnelOwner(owner string) FunnelOption {
	return func(c *funnelConfig) { c.Owner = owner }
}

// WithFunnelHide sets whether this lease should be hidden from the public listing.
func WithFunnelHide(hide bool) FunnelOption {
	return func(c *funnelConfig) { c.Hide = hide }
}

// WithFunnelWorkers sets the number of reverse connection workers.
func WithFunnelWorkers(n int) FunnelOption {
	return func(c *funnelConfig) { c.Workers = n }
}

// FunnelClient manages registration and reverse connections for funnel mode.
type FunnelClient struct {
	relayURL   string // Base HTTP URL of the relay (e.g. "http://localhost:4017").
	httpClient *http.Client
}

// NewFunnelClient creates a funnel client targeting the given relay URL.
func NewFunnelClient(relayURL string) *FunnelClient {
	return &FunnelClient{
		relayURL:   strings.TrimRight(relayURL, "/"),
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Register registers a funnel lease with the relay and returns a FunnelListener
// that implements net.Listener. The listener manages reverse workers, keepalive,
// and TLS termination.
func (c *FunnelClient) Register(name string, opts ...FunnelOption) (*FunnelListener, error) {
	cfg := &funnelConfig{Workers: 4}
	for _, o := range opts {
		o(cfg)
	}

	// Build metadata JSON.
	var metadata string
	if cfg.Description != "" || len(cfg.Tags) > 0 || cfg.Thumbnail != "" || cfg.Owner != "" || cfg.Hide {
		meta := map[string]any{}
		if cfg.Description != "" {
			meta["description"] = cfg.Description
		}
		if len(cfg.Tags) > 0 {
			meta["tags"] = cfg.Tags
		}
		if cfg.Thumbnail != "" {
			meta["thumbnail"] = cfg.Thumbnail
		}
		if cfg.Owner != "" {
			meta["owner"] = cfg.Owner
		}
		if cfg.Hide {
			meta["hide"] = cfg.Hide
		}
		b, _ := json.Marshal(meta)
		metadata = string(b)
	}

	// POST /api/register.
	regReq := api.RegisterRequest{Name: name, Metadata: metadata}
	body, _ := json.Marshal(regReq)

	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.relayURL+"/api/register", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("register request creation failed: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("register request failed: %w", err)
	}
	defer resp.Body.Close()

	var regResp api.RegisterResponse
	err = json.NewDecoder(resp.Body).Decode(&regResp)
	if err != nil {
		return nil, fmt.Errorf("register response decode failed: %w", err)
	}
	if !regResp.Success {
		return nil, fmt.Errorf("%w: %s", ErrRegistrationFail, regResp.Message)
	}

	// Parse TLS cert+key received from relay.
	tlsCert, err := tls.X509KeyPair([]byte(regResp.TLSCert), []byte(regResp.TLSKey))
	if err != nil {
		return nil, fmt.Errorf("invalid TLS certificate from relay: %w", err)
	}

	l := &FunnelListener{
		client:       c,
		name:         name,
		metadata:     metadata,
		leaseID:      regResp.LeaseID,
		reverseToken: regResp.ReverseToken,
		publicURL:    regResp.PublicURL,
		acceptCh:     make(chan net.Conn, 64),
		stopCh:       make(chan struct{}),
		workers:      cfg.Workers,
	}

	// Store initial cert atomically.
	l.tlsCert.Store(&tlsCert)

	// Build TLS config with GetCertificate callback for thread-safe cert rotation.
	l.tlsConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert := l.tlsCert.Load()
			if cert == nil {
				return nil, errors.New("no TLS certificate available")
			}
			return cert, nil
		},
	}

	// Start reverse workers.
	for i := range l.workers {
		l.wg.Add(1)
		go l.reverseAcceptWorker(i)
	}

	// Start keepalive.
	l.wg.Add(1)
	go l.keepaliveLoop()

	log.Info().
		Str("lease_id", l.leaseID).
		Str("public_url", l.publicURL).
		Int("workers", l.workers).
		Msg("[FunnelSDK] listener started")

	return l, nil
}

// FunnelListener implements net.Listener for funnel mode.
// Accept() returns TLS-terminated connections from browsers via the relay.
var _ net.Listener = (*FunnelListener)(nil)

// FunnelListener wraps reverse connections from the relay with
// TLS termination, exposing them through the standard net.Listener interface.
type FunnelListener struct {
	client   *FunnelClient
	name     string
	metadata string

	// mu protects leaseID, reverseToken, publicURL from concurrent access
	// during re-registration.
	mu           sync.RWMutex
	leaseID      string
	reverseToken string
	publicURL    string

	tlsCert   atomic.Pointer[tls.Certificate] // atomic swap for thread-safe cert updates
	tlsConfig *tls.Config                     // immutable after init; uses GetCertificate

	acceptCh chan net.Conn
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	workers  int
}

// Accept blocks until a TLS-terminated connection is available or the listener is closed.
func (l *FunnelListener) Accept() (net.Conn, error) {
	for {
		select {
		case <-l.stopCh:
			return nil, ErrFunnelClosed
		case rawConn, ok := <-l.acceptCh:
			if !ok {
				return nil, ErrFunnelClosed
			}
			// TLS termination directly on the raw net.Conn.
			tlsConn := tls.Server(rawConn, l.tlsConfig)
			if err := tlsConn.HandshakeContext(context.Background()); err != nil {
				log.Debug().Err(err).Msg("[FunnelSDK] TLS handshake failed")
				_ = rawConn.Close()
				continue
			}
			return tlsConn, nil
		}
	}
}

// Close gracefully shuts down the listener: unregisters from relay, stops workers.
func (l *FunnelListener) Close() error {
	firstClose := false
	l.stopOnce.Do(func() {
		firstClose = true
		close(l.stopCh)
	})
	if !firstClose {
		return ErrFunnelClosed
	}

	leaseID, reverseToken := l.getCredentials()

	// Unregister from relay (best-effort).
	unregBody := api.UnregisterRequest{LeaseID: leaseID, ReverseToken: reverseToken}
	body, _ := json.Marshal(unregBody)
	httpReq, reqErr := http.NewRequestWithContext(context.Background(), http.MethodPost, l.client.relayURL+"/api/unregister", bytes.NewReader(body))
	if reqErr != nil {
		log.Warn().Err(reqErr).Msg("[FunnelSDK] unregister request creation failed")
	} else {
		httpReq.Header.Set("Content-Type", "application/json")
		resp, doErr := l.client.httpClient.Do(httpReq)
		if doErr != nil {
			log.Warn().Err(doErr).Msg("[FunnelSDK] unregister failed")
		} else {
			_ = resp.Body.Close()
		}
	}

	l.wg.Wait()
	close(l.acceptCh)

	log.Info().Str("lease_id", leaseID).Msg("[FunnelSDK] listener closed")
	return nil
}

// Addr returns a synthetic net.Addr with the public URL.
func (l *FunnelListener) Addr() net.Addr {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return funnelAddr(l.publicURL)
}

// PublicURL returns the public HTTPS URL for this funnel.
func (l *FunnelListener) PublicURL() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.publicURL
}

// LeaseID returns the lease ID assigned by the relay.
func (l *FunnelListener) LeaseID() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.leaseID
}

// getCredentials returns the current leaseID and reverseToken under read lock.
func (l *FunnelListener) getCredentials() (leaseID, reverseToken string) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.leaseID, l.reverseToken
}

// keepaliveLoop sends POST /api/renew every 10s.
func (l *FunnelListener) keepaliveLoop() {
	defer l.wg.Done()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			renewErr := l.renew()
			if renewErr != nil {
				leaseID, _ := l.getCredentials()
				log.Warn().Err(renewErr).Str("lease_id", leaseID).Msg("[FunnelSDK] renew failed")
				// If lease not found, attempt re-registration.
				if isLeaseNotFound(renewErr) {
					reRegErr := l.reRegister()
					if reRegErr != nil {
						log.Error().Err(reRegErr).Msg("[FunnelSDK] re-registration failed")
					}
				}
			}
		}
	}
}

func (l *FunnelListener) renew() error {
	leaseID, reverseToken := l.getCredentials()
	renewBody := api.RenewRequest{LeaseID: leaseID, ReverseToken: reverseToken}
	body, _ := json.Marshal(renewBody)
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, l.client.relayURL+"/api/renew", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := l.client.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result api.RenewResponse
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return err
	}
	if !result.Success {
		return fmt.Errorf("renew: %s", result.Message)
	}
	return nil
}

func (l *FunnelListener) reRegister() error {
	log.Info().Str("name", l.name).Msg("[FunnelSDK] re-registering lease")

	regReq := api.RegisterRequest{Name: l.name, Metadata: l.metadata}
	body, _ := json.Marshal(regReq)
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, l.client.relayURL+"/api/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := l.client.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var regResp api.RegisterResponse
	err = json.NewDecoder(resp.Body).Decode(&regResp)
	if err != nil {
		return err
	}
	if !regResp.Success {
		return fmt.Errorf("re-register: %s", regResp.Message)
	}

	// Update credentials under write lock.
	l.mu.Lock()
	l.leaseID = regResp.LeaseID
	l.reverseToken = regResp.ReverseToken
	l.publicURL = regResp.PublicURL
	l.mu.Unlock()

	// Update TLS cert if changed.
	if regResp.TLSCert != "" && regResp.TLSKey != "" {
		certErr := l.updateCert(regResp.TLSCert, regResp.TLSKey)
		if certErr != nil {
			log.Warn().Err(certErr).Msg("[FunnelSDK] TLS cert update failed")
		}
	}

	log.Info().Str("lease_id", regResp.LeaseID).Str("public_url", regResp.PublicURL).Msg("[FunnelSDK] re-registration successful")
	return nil
}

func (l *FunnelListener) updateCert(certPEM, keyPEM string) error {
	tlsCert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return err
	}
	l.tlsCert.Store(&tlsCert)
	return nil
}

// reverseAcceptWorker continuously establishes reverse connections
// to the relay, waits for the start marker, and pushes them to acceptCh.
func (l *FunnelListener) reverseAcceptWorker(workerID int) {
	defer l.wg.Done()

	for {
		select {
		case <-l.stopCh:
			return
		default:
		}

		conn, dialErr := l.dialReverse()
		if dialErr != nil {
			log.Debug().Err(dialErr).Int("worker", workerID).Msg("[FunnelSDK] reverse dial failed, retrying")
			select {
			case <-l.stopCh:
				return
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}

		// Wait for start marker (0x01).
		if markerErr := l.waitForStartMarker(conn); markerErr != nil {
			log.Debug().Err(markerErr).Int("worker", workerID).Msg("[FunnelSDK] start marker wait failed")
			_ = conn.Close()
			continue
		}

		// Push to accept channel.
		select {
		case <-l.stopCh:
			_ = conn.Close()
			return
		case l.acceptCh <- conn:
		}
	}
}

// dialReverse opens a raw TCP connection to /api/connect?lease_id=X&token=Y
// via HTTP Upgrade, returning the hijacked net.Conn.
func (l *FunnelListener) dialReverse() (net.Conn, error) {
	leaseID, reverseToken := l.getCredentials()

	u, err := url.Parse(l.client.relayURL)
	if err != nil {
		return nil, fmt.Errorf("invalid relay URL: %w", err)
	}

	host := u.Host
	if !strings.Contains(host, ":") {
		if u.Scheme == "https" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	// Dial TCP to relay.
	rawConn, err := net.DialTimeout("tcp", host, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial relay: %w", err)
	}

	// Wrap with TLS if relay URL is HTTPS.
	if u.Scheme == "https" {
		hostname, _, _ := net.SplitHostPort(host)
		tlsConn := tls.Client(rawConn, &tls.Config{ServerName: hostname})
		if err := tlsConn.HandshakeContext(context.Background()); err != nil {
			_ = rawConn.Close()
			return nil, fmt.Errorf("tls handshake with relay: %w", err)
		}
		rawConn = tlsConn
	}

	// Send HTTP Upgrade request.
	query := url.Values{"lease_id": {leaseID}, "token": {reverseToken}}.Encode()
	reqLine := fmt.Sprintf("GET /api/connect?%s HTTP/1.1\r\nHost: %s\r\nUpgrade: portal-reverse/1\r\nConnection: Upgrade\r\n\r\n", query, u.Host)
	if _, err := rawConn.Write([]byte(reqLine)); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("write upgrade request: %w", err)
	}

	// Read 101 Switching Protocols response.
	br := bufio.NewReader(rawConn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("read upgrade response: %w", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = rawConn.Close()
		return nil, fmt.Errorf("upgrade failed: status %d", resp.StatusCode)
	}

	// Return buffered conn to preserve any data already in br's buffer.
	return &bufferedConn{Conn: rawConn, r: br}, nil
}

// waitForStartMarker blocks until the relay sends 0x01, indicating
// a browser has connected and data forwarding should begin.
func (l *FunnelListener) waitForStartMarker(conn net.Conn) error {
	buf := make([]byte, 1)
	for {
		select {
		case <-l.stopCh:
			return ErrFunnelClosed
		default:
		}

		// Use a short read to avoid blocking forever.
		n, readErr := conn.Read(buf)
		if readErr != nil {
			return readErr
		}
		if n == 1 && buf[0] == 0x01 {
			return nil
		}
	}
}

// --- Helper types and functions ---

// bufferedConn wraps a net.Conn with a bufio.Reader so that any data
// consumed into the buffer during HTTP response parsing is not lost.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	return c.r.Read(p)
}

type funnelAddr string

func (a funnelAddr) Network() string { return "funnel" }
func (a funnelAddr) String() string  { return string(a) }

func isLeaseNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "lease not found")
}
