package sdk

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdverb"
)

var (
	ErrNoAvailableRelay     = errors.New("no available relay")
	ErrClientClosed         = errors.New("client is closed")
	ErrListenerExists       = errors.New("listener already exists for this credential")
	ErrRelayExists          = errors.New("relay already exists")
	ErrRelayNotFound        = errors.New("relay not found")
	ErrInvalidName          = errors.New("lease name contains invalid characters (only alphanumeric, hyphen, underscore allowed)")
	ErrFailedToCreateClient = errors.New("failed to create relay client")
	ErrInvalidMetadata      = errors.New("invalid metadata")
)

type ClientConfig struct {
	BootstrapServers    []string
	Dialer              func(context.Context, string) (io.ReadWriteCloser, error)
	HealthCheckInterval time.Duration // Interval for health checks (default: 10 seconds)
	ReconnectMaxRetries int           // Maximum reconnection attempts (default: 0 = infinite)
	ReconnectInterval   time.Duration // Interval between reconnection attempts (default: 5 seconds)
}

type ClientOption func(*ClientConfig)

func WithBootstrapServers(servers []string) ClientOption {
	return func(c *ClientConfig) {
		c.BootstrapServers = servers
	}
}

func WithDialer(dialer func(context.Context, string) (io.ReadWriteCloser, error)) ClientOption {
	return func(c *ClientConfig) {
		c.Dialer = dialer
	}
}

func WithHealthCheckInterval(interval time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.HealthCheckInterval = interval
	}
}

func WithReconnectMaxRetries(retries int) ClientOption {
	return func(c *ClientConfig) {
		c.ReconnectMaxRetries = retries
	}
}

func WithReconnectInterval(interval time.Duration) ClientOption {
	return func(c *ClientConfig) {
		c.ReconnectInterval = interval
	}
}

type Metadata struct {
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Thumbnail   string   `json:"thumbnail"`
	Owner       string   `json:"owner"`
	Hide        bool     `json:"hide"`
}

func (m Metadata) isEmpty() bool {
	return m.Description == "" &&
		len(m.Tags) == 0 &&
		m.Thumbnail == "" &&
		m.Owner == ""
}

type MetadataOption func(*Metadata)

func WithDescription(description string) MetadataOption {
	return func(m *Metadata) {
		m.Description = description
	}
}

func WithTags(tags []string) MetadataOption {
	return func(m *Metadata) {
		m.Tags = tags
	}
}

func WithThumbnail(thumbnail string) MetadataOption {
	return func(m *Metadata) {
		m.Thumbnail = thumbnail
	}
}

func WithOwner(owner string) MetadataOption {
	return func(m *Metadata) {
		m.Owner = owner
	}
}

func WithHide(hide bool) MetadataOption {
	return func(m *Metadata) {
		m.Hide = hide
	}
}

type listener struct {
	mu sync.Mutex

	cred  *cryptoops.Credential
	lease *rdverb.Lease

	conns map[*connection]struct{}

	connCh chan *connection
	closed bool
}

// Implement net.Listener interface for Listener
func (l *listener) Accept() (net.Conn, error) {
	conn, ok := <-l.connCh
	if !ok {
		return nil, net.ErrClosed
	}
	return conn, nil
}

func (l *listener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}

	l.closed = true

	// Close the connection channel first to prevent new connections
	close(l.connCh)

	// Close all active connections
	for conn := range l.conns {
		if err := conn.Close(); err != nil {
			log.Error().Err(err).Msg("[SDK] Error closing connection")
		}
		delete(l.conns, conn)
	}

	// Clear the connections map
	l.conns = make(map[*connection]struct{})

	return nil
}

func (l *listener) Addr() net.Addr {
	return addr(l.cred.ID())
}

type connRelay struct {
	addr     string
	client   *portal.RelayClient
	dialer   func(context.Context, string) (io.ReadWriteCloser, error)
	stop     chan struct{}
	stopOnce sync.Once // Ensure stop channel is closed only once
	mu       sync.Mutex
}

var _ net.Conn = (*connection)(nil)

type connection struct {
	via        *connRelay
	localAddr  string
	remoteAddr string
	conn       *cryptoops.SecureConnection
}

func (r *connection) Read(b []byte) (n int, err error) {
	return r.conn.Read(b)
}

func (r *connection) Write(b []byte) (n int, err error) {
	return r.conn.Write(b)
}

func (r *connection) Close() error {
	return r.conn.Close()
}

func (r *connection) LocalAddr() net.Addr {
	return addr(r.localAddr)
}

func (r *connection) RemoteAddr() net.Addr {
	return addr(r.remoteAddr)
}

func (r *connection) SetDeadline(t time.Time) error {
	return r.conn.SetDeadline(t)
}

func (r *connection) SetReadDeadline(t time.Time) error {
	return r.conn.SetReadDeadline(t)
}

func (r *connection) SetWriteDeadline(t time.Time) error {
	return r.conn.SetWriteDeadline(t)
}

var _ net.Addr = (*addr)(nil)

type addr string

func (a addr) Network() string {
	return "portal"
}

func (a addr) String() string {
	return string(a)
}
