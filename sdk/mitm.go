package sdk

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

const (
	mitmProbeExporterLabel = "Portal-MITM-Probe-v1"
	mitmProbePeekTimeout   = 100 * time.Millisecond
	mitmProbePaddingMin    = 96
	mitmProbePaddingMax    = 320

	defaultMITMProbeCooldown = 30 * time.Second
	defaultMITMProbeTimeout  = 5 * time.Second
)

type MITMProbeReport struct {
	RelayURL  string
	PublicURL string
	LeaseID   string
	CheckedAt time.Time
	Detected  bool
	Reason    string
}

type mitmProbePending struct {
	expected []byte
	resultCh chan mitmProbeResult
}

type mitmProbeResult struct {
	matched bool
	reason  string
}

type mitmManager struct {
	ctx      context.Context
	listener *Listener

	mu       sync.Mutex
	pending  map[string]*mitmProbePending
	inFlight bool
	lastAt   time.Time
}

func newMITMManager(ctx context.Context, listener *Listener) *mitmManager {
	return &mitmManager{
		ctx:      ctx,
		listener: listener,
		pending:  make(map[string]*mitmProbePending),
	}
}

func (m *mitmManager) reset() {
	m.mu.Lock()
	clear(m.pending)
	m.inFlight = false
	m.lastAt = time.Time{}
	m.mu.Unlock()
}

func (m *mitmManager) probeTLSPassthrough(ctx context.Context) (MITMProbeReport, error) {
	report := MITMProbeReport{
		CheckedAt: time.Now(),
	}
	l := m.listener
	if l == nil || l.api == nil || l.api.baseURL == nil {
		return report, errors.New("listener is not ready")
	}

	publicURL := l.PublicURL()
	if publicURL == "" {
		return report, errors.New("listener is not registered")
	}

	hostname := l.Hostname()
	if hostname == "" {
		return report, errors.New("listener hostname is unavailable")
	}

	probeCtx, cancel := context.WithTimeout(ctx, defaultMITMProbeTimeout)
	defer cancel()

	nonceRaw := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, nonceRaw); err != nil {
		return report, fmt.Errorf("generate probe nonce: %w", err)
	}
	nonceHex := hex.EncodeToString(nonceRaw)

	report.RelayURL = l.api.baseURL.String()
	report.PublicURL = publicURL
	report.LeaseID = l.LeaseID()

	parsedURL, err := url.Parse(publicURL)
	if err != nil {
		return report, fmt.Errorf("parse public url: %w", err)
	}

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: l.api.dialTimeout},
		Config:    m.clientTLSConfig(hostname),
	}
	conn, err := dialer.DialContext(probeCtx, "tcp", utils.EnsurePort(parsedURL.Host))
	if err != nil {
		return report, fmt.Errorf("dial mitm probe: %w", err)
	}
	defer conn.Close()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return report, errors.New("mitm probe connection is not tls")
	}

	clientState := tlsConn.ConnectionState()
	expected, err := (&clientState).ExportKeyingMaterial(mitmProbeExporterLabel, nil, 32)
	if err != nil {
		return report, fmt.Errorf("export client probe keying material: %w", err)
	}
	resultCh, cleanupProbe := m.startProbe(nonceHex, expected)
	defer cleanupProbe()

	paddingLen := mitmProbePaddingMin
	if mitmProbePaddingMax > mitmProbePaddingMin {
		var paddingSeed [1]byte
		if _, err := io.ReadFull(rand.Reader, paddingSeed[:]); err != nil {
			return report, fmt.Errorf("generate probe padding length: %w", err)
		}
		paddingLen += int(paddingSeed[0]) % (mitmProbePaddingMax - mitmProbePaddingMin + 1)
	}

	frame := make([]byte, len(nonceRaw)+paddingLen)
	copy(frame, nonceRaw)
	if _, err := io.ReadFull(rand.Reader, frame[len(nonceRaw):]); err != nil {
		return report, fmt.Errorf("generate probe padding: %w", err)
	}
	if _, err := conn.Write(frame); err != nil {
		return report, fmt.Errorf("write mitm probe: %w", err)
	}

	select {
	case result := <-resultCh:
		report.Detected = !result.matched
		report.Reason = result.reason
		return report, nil
	case <-probeCtx.Done():
		report.Detected = false
		report.Reason = types.MITMProbeReasonProbeTimeout
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			return report, nil
		}
		return report, probeCtx.Err()
	}
}

func (m *mitmManager) clientTLSConfig(hostname string) *tls.Config {
	probeTLSConf := &tls.Config{
		ServerName:         hostname,
		InsecureSkipVerify: true,
	}

	l := m.listener
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.tlsConfig != nil {
		probeTLSConf.MinVersion = l.tlsConfig.MinVersion
		probeTLSConf.MaxVersion = l.tlsConfig.MaxVersion
		if len(l.tlsConfig.NextProtos) > 0 {
			probeTLSConf.NextProtos = append([]string(nil), l.tlsConfig.NextProtos...)
		}
	}
	return probeTLSConf
}

func (m *mitmManager) maybeStart() {
	l := m.listener
	if l.closed() {
		return
	}

	m.mu.Lock()
	if m.inFlight || !m.lastAt.IsZero() && time.Since(m.lastAt) < defaultMITMProbeCooldown {
		m.mu.Unlock()
		return
	}
	m.inFlight = true
	m.mu.Unlock()

	go func() {
		report, err := m.probeTLSPassthrough(m.ctx)
		m.finish(err == nil && report.Reason != types.MITMProbeReasonProbeTimeout)
		m.logResult(report, err)
	}()
}

func (m *mitmManager) finish(success bool) {
	m.mu.Lock()
	m.inFlight = false
	if success {
		m.lastAt = time.Now()
	}
	m.mu.Unlock()
}

func (m *mitmManager) logResult(report MITMProbeReport, err error) {
	l := m.listener
	switch {
	case l.closed():
		return
	case err != nil:
		if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
			return
		}
		log.Warn().
			Err(err).
			Str("relay_url", l.api.baseURL.String()).
			Str("lease_id", l.LeaseID()).
			Msg("tls passthrough self-probe failed")
	case report.Reason == types.MITMProbeReasonProbeTimeout:
		log.Warn().
			Str("relay_url", report.RelayURL).
			Str("public_url", report.PublicURL).
			Str("lease_id", report.LeaseID).
			Msg("tls self-probe timed out before passthrough could be verified")
	case report.Detected:
		log.Warn().
			Str("reason", report.Reason).
			Str("relay_url", report.RelayURL).
			Str("public_url", report.PublicURL).
			Str("lease_id", report.LeaseID).
			Msg("tls termination suspected by self-probe")
	default:
		log.Debug().
			Str("relay_url", report.RelayURL).
			Str("public_url", report.PublicURL).
			Str("lease_id", report.LeaseID).
			Msg("tls passthrough self-probe passed")
	}
}

func (m *mitmManager) maybeHandleConn(conn net.Conn) (net.Conn, bool, error) {
	if conn == nil {
		return conn, false, nil
	}

	m.mu.Lock()
	hasPending := len(m.pending) > 0
	m.mu.Unlock()
	if !hasPending {
		return conn, false, nil
	}

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return conn, false, nil
	}

	frameSize := 16
	reader := bufio.NewReaderSize(conn, frameSize)
	_ = conn.SetReadDeadline(time.Now().Add(mitmProbePeekTimeout))
	peeked, err := reader.Peek(frameSize)
	defer conn.SetReadDeadline(time.Time{})
	if err != nil {
		return wrapBufferedConn(conn, reader), false, nil
	}

	nonceHex := hex.EncodeToString(peeked[:frameSize])
	m.mu.Lock()
	_, ok = m.pending[nonceHex]
	m.mu.Unlock()
	if !ok {
		return wrapBufferedConn(conn, reader), false, nil
	}

	defer conn.Close()

	frame := make([]byte, frameSize)
	if _, err := io.ReadFull(reader, frame); err != nil {
		return nil, true, fmt.Errorf("read mitm probe frame: %w", err)
	}

	serverState := tlsConn.ConnectionState()
	actual, err := (&serverState).ExportKeyingMaterial(mitmProbeExporterLabel, nil, 32)
	if err != nil {
		return nil, true, fmt.Errorf("export server probe keying material: %w", err)
	}

	m.completeProbe(nonceHex, actual)
	return nil, true, nil
}

func (m *mitmManager) startProbe(nonce string, expected []byte) (<-chan mitmProbeResult, func()) {
	m.mu.Lock()
	state := &mitmProbePending{
		expected: append([]byte(nil), expected...),
		resultCh: make(chan mitmProbeResult, 1),
	}
	m.pending[nonce] = state
	m.mu.Unlock()

	return state.resultCh, func() {
		m.mu.Lock()
		delete(m.pending, nonce)
		m.mu.Unlock()
	}
}

func (m *mitmManager) completeProbe(nonce string, actual []byte) {
	m.mu.Lock()
	state := m.pending[nonce]
	m.mu.Unlock()
	if state == nil {
		return
	}

	result := mitmProbeResult{
		matched: bytes.Equal(state.expected, actual),
	}
	if !result.matched {
		result.reason = types.MITMProbeReasonExporterMismatch
	}

	select {
	case state.resultCh <- result:
	default:
	}
}

func wrapMITMProbeConn(manager *mitmManager, conn net.Conn) net.Conn {
	if conn == nil {
		return conn
	}
	return &mitmProbeConn{Conn: conn, manager: manager}
}

type mitmProbeConn struct {
	net.Conn
	manager   *mitmManager
	startOnce sync.Once
}

func (c *mitmProbeConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.startOnce.Do(func() {
			c.manager.maybeStart()
		})
	}
	return n, err
}

func (c *mitmProbeConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.startOnce.Do(func() {
			c.manager.maybeStart()
		})
	}
	return n, err
}
