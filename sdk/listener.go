package sdk

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/types"
)

type ListenRequest struct {
	Name         string
	ReverseToken string
	Hostnames    []string
	Metadata     types.LeaseMetadata
	ReadyTarget  int
	LeaseTTL     time.Duration
}

type Listener struct {
	tlsCloser    io.Closer
	tlsConfig    *tls.Config
	baseContext  func() context.Context
	ctxDone      <-chan struct{}
	cancel       context.CancelFunc
	client       *Client
	signal       chan struct{}
	accepted     chan net.Conn
	name         string
	leaseID      string
	reverseToken string
	hostnames    []string
	metadata     types.LeaseMetadata
	readyTarget  int
	leaseTTL     time.Duration

	activeSessions int
	closeOnce      sync.Once
	mu             sync.Mutex
}

func (l *Listener) Accept() (net.Conn, error) {
	select {
	case <-l.ctxDone:
		return nil, net.ErrClosed
	case conn := <-l.accepted:
		if conn == nil {
			return nil, net.ErrClosed
		}
		return conn, nil
	}
}

func (l *Listener) Close() error {
	var closeErr error
	l.closeOnce.Do(func() {
		l.cancel()

		l.mu.Lock()
		leaseID := l.leaseID
		tlsCloser := l.tlsCloser
		l.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := l.client.unregisterLease(ctx, leaseID, l.reverseToken); err != nil {
			closeErr = err
		}
		if tlsCloser != nil {
			closeErr = errors.Join(closeErr, tlsCloser.Close())
		}
	})
	return closeErr
}

func (l *Listener) Addr() net.Addr {
	return listenerAddr("portal:" + l.leaseID)
}

func (l *Listener) LeaseID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.leaseID
}

func (l *Listener) Hostnames() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hostnames
}

func (l *Listener) Metadata() types.LeaseMetadata {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.metadata
}

func (l *Listener) PublicURLs() []string {
	l.mu.Lock()
	hostnames := l.hostnames
	l.mu.Unlock()

	urls := make([]string, 0, len(hostnames))
	for _, host := range hostnames {
		urls = append(urls, "https://"+host)
	}
	return urls
}

func (l *Listener) runSupervisor() {
	for {
		select {
		case <-l.ctxDone:
			return
		case <-l.signal:
		}

		for l.reserveSessionSlot() {
			go l.runSession()
		}
	}
}

func (l *Listener) runRenewLoop() {
	interval := l.leaseTTL / 2
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if l.client.renewBefore > 0 && l.leaseTTL > l.client.renewBefore {
		interval = l.leaseTTL - l.client.renewBefore
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var consecutiveFailures int

	for {
		select {
		case <-l.ctxDone:
			return
		case <-ticker.C:
			l.mu.Lock()
			leaseID := l.leaseID
			l.mu.Unlock()

			ctx, cancel := context.WithTimeout(l.context(), 10*time.Second)
			err := l.client.renewLease(ctx, leaseID, l.reverseToken, l.leaseTTL)
			cancel()

			if err != nil {
				if isLeaseNotFound(err) {
					log.Warn().
						Str("component", "sdk-listener").
						Str("lease_id", leaseID).
						Msg("lease not found on relay, attempting re-registration")
					if reregErr := l.reregister(); reregErr != nil {
						log.Error().Err(reregErr).
							Str("component", "sdk-listener").
							Msg("lease re-registration failed")
					} else {
						consecutiveFailures = 0
						log.Info().
							Str("component", "sdk-listener").
							Str("lease_id", l.LeaseID()).
							Strs("hostnames", l.Hostnames()).
							Msg("lease re-registered successfully")
						continue
					}
				}

				consecutiveFailures++
				event := log.Warn()
				if consecutiveFailures >= 3 {
					event = log.Error()
				}
				event.Err(err).
					Str("component", "sdk-listener").
					Str("lease_id", l.LeaseID()).
					Int("consecutive_failures", consecutiveFailures).
					Msg("lease renewal failed")
			} else {
				consecutiveFailures = 0
			}
		}
	}
}

func (l *Listener) runSession() {
	defer l.releaseSessionSlot()

	sessionCtx := l.context()
	l.mu.Lock()
	leaseID := l.leaseID
	l.mu.Unlock()
	conn, err := l.client.openReverseSession(sessionCtx, leaseID, l.reverseToken)
	if err != nil {
		sleepOrDone(sessionCtx, time.Second)
		return
	}

	if err := l.awaitActivation(conn); err != nil {
		_ = conn.Close()
		if !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
			sleepOrDone(sessionCtx, time.Second)
		}
	}
}

func (l *Listener) awaitActivation(conn net.Conn) error {
	var marker [1]byte
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * l.client.handshakeTimeout))
		if _, err := io.ReadFull(conn, marker[:]); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Time{})

		switch marker[0] {
		case types.MarkerKeepalive:
			continue
		case types.MarkerTLSStart:
			return l.activate(conn)
		default:
			return fmt.Errorf("unexpected reverse marker: 0x%02x", marker[0])
		}
	}
}

func (l *Listener) activate(conn net.Conn) error {
	l.mu.Lock()
	tlsCfg := l.tlsConfig
	l.mu.Unlock()
	// Reuse the shared config so session ticket state survives across connections.
	tlsConn := tls.Server(conn, tlsCfg)
	handshakeCtx, cancel := context.WithTimeout(l.context(), l.client.handshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		return err
	}

	select {
	case <-l.ctxDone:
		_ = tlsConn.Close()
		return l.context().Err()
	case l.accepted <- tlsConn:
		return nil
	}
}

func (l *Listener) reregister() error {
	ctx, cancel := context.WithTimeout(l.context(), 10*time.Second)
	defer cancel()

	l.mu.Lock()
	hostnames := l.hostnames
	l.mu.Unlock()

	resp, err := l.client.registerLease(ctx, types.RegisterRequest{
		Name:         l.name,
		Hostnames:    hostnames,
		Metadata:     l.metadata,
		ReverseToken: l.reverseToken,
		TLS:          true,
		TTLSeconds:   int(l.leaseTTL / time.Second),
	})
	if err != nil {
		return err
	}

	tlsConf, tlsCloser, err := keyless.BuildClientTLSConfig(l.client.baseURL.String(), resp.Hostnames)
	if err != nil {
		_ = l.client.unregisterLease(ctx, resp.LeaseID, l.reverseToken)
		return err
	}

	l.mu.Lock()
	oldCloser := l.tlsCloser
	l.leaseID = resp.LeaseID
	l.hostnames = resp.Hostnames
	l.metadata = resp.Metadata
	l.tlsConfig = tlsConf
	l.tlsCloser = tlsCloser
	l.mu.Unlock()

	if oldCloser != nil {
		_ = oldCloser.Close()
	}

	l.notify()
	return nil
}

func isLeaseNotFound(err error) bool {
	return errors.Is(err, &types.APIRequestError{Code: types.APIErrorCodeLeaseNotFound})
}

func (l *Listener) reserveSessionSlot() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.isClosed() {
		return false
	}
	if l.activeSessions >= l.readyTarget {
		return false
	}
	l.activeSessions++
	return true
}

func (l *Listener) releaseSessionSlot() {
	l.mu.Lock()
	l.activeSessions--
	l.mu.Unlock()
	l.notify()
}

func (l *Listener) notify() {
	select {
	case l.signal <- struct{}{}:
	default:
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

type listenerAddr string

func (a listenerAddr) Network() string { return "portal" }
func (a listenerAddr) String() string  { return string(a) }

func (l *Listener) context() context.Context {
	if l.baseContext != nil {
		if ctx := l.baseContext(); ctx != nil {
			return ctx
		}
	}
	return context.Background()
}

func (l *Listener) isClosed() bool {
	if l.ctxDone == nil {
		return false
	}
	select {
	case <-l.ctxDone:
		return true
	default:
		return false
	}
}
