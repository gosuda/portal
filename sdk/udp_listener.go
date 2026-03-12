package sdk

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/types"
)

// UDPListener manages a QUIC connection to the relay for a UDP-transport lease.
// It receives DATAGRAM frames from the relay and delivers decoded datagrams via
// the AcceptDatagram method.
type UDPListener struct {
	client       *Client
	baseContext  func() context.Context
	ctxDone      <-chan struct{}
	cancel       context.CancelFunc

	name         string
	leaseID      string
	reverseToken string
	udpAddr      string
	quicAddr     string
	hostnames    []string
	metadata     types.LeaseMetadata
	leaseTTL     time.Duration

	conn      *quic.Conn
	datagrams chan UDPDatagram
	done      chan struct{}

	ownsLease bool
	closeOnce sync.Once
	mu        sync.Mutex
}

// UDPDatagram represents a single datagram received from a public client
// through the relay.
type UDPDatagram struct {
	FlowID  uint32
	Payload []byte
}

// AcceptDatagram blocks until a datagram is available or the listener is closed.
func (l *UDPListener) AcceptDatagram() (UDPDatagram, error) {
	select {
	case <-l.ctxDone:
		return UDPDatagram{}, net.ErrClosed
	case dg, ok := <-l.datagrams:
		if !ok {
			return UDPDatagram{}, net.ErrClosed
		}
		return dg, nil
	}
}

// SendDatagram sends a response datagram back to a client via the relay.
func (l *UDPListener) SendDatagram(flowID uint32, payload []byte) error {
	l.mu.Lock()
	conn := l.conn
	l.mu.Unlock()

	if conn == nil {
		return errors.New("quic connection not established")
	}
	return conn.SendDatagram(encodeDatagram(flowID, payload))
}

// UDPAddr returns the public UDP address allocated by the relay.
func (l *UDPListener) UDPAddr() string {
	return l.udpAddr
}

// LeaseID returns the current lease ID.
func (l *UDPListener) LeaseID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.leaseID
}

// Hostnames returns the hostnames registered for this lease.
func (l *UDPListener) Hostnames() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hostnames
}

// Close tears down the QUIC connection. If this listener owns the lease
// (created via ListenUDP), it also unregisters the lease. Attached listeners
// (created via AttachUDP) leave lease lifecycle to the TCP Listener.
func (l *UDPListener) Close() error {
	var closeErr error
	l.closeOnce.Do(func() {
		l.cancel()

		l.mu.Lock()
		conn := l.conn
		leaseID := l.leaseID
		l.mu.Unlock()

		if conn != nil {
			_ = conn.CloseWithError(0, "listener closed")
		}

		if l.ownsLease {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			closeErr = l.client.unregisterLease(ctx, leaseID, l.reverseToken)
		}
	})
	return closeErr
}

func (l *UDPListener) runSupervisor() {
	for {
		select {
		case <-l.ctxDone:
			return
		default:
		}

		conn, err := l.client.openQUICSession(l.context(), l.quicAddr, l.leaseID, l.reverseToken)
		if err != nil {
			log.Warn().
				Err(err).
				Str("component", "sdk-udp-listener").
				Str("lease_id", l.leaseID).
				Msg("quic session open failed, retrying")
			sleepOrDone(l.context(), 2*time.Second)
			continue
		}

		l.mu.Lock()
		l.conn = conn
		l.mu.Unlock()

		log.Info().
			Str("component", "sdk-udp-listener").
			Str("lease_id", l.leaseID).
			Str("remote_addr", conn.RemoteAddr().String()).
			Msg("quic tunnel connected")

		l.receiveLoop(conn)

		l.mu.Lock()
		if l.conn == conn {
			l.conn = nil
		}
		l.mu.Unlock()

		if l.isClosed() {
			return
		}
		sleepOrDone(l.context(), time.Second)
	}
}

func (l *UDPListener) receiveLoop(conn *quic.Conn) {
	for {
		data, err := conn.ReceiveDatagram(l.context())
		if err != nil {
			if !l.isClosed() {
				log.Warn().
					Err(err).
					Str("component", "sdk-udp-listener").
					Str("lease_id", l.leaseID).
					Msg("quic receive loop ended")
			}
			return
		}

		frame, err := decodeDatagram(data)
		if err != nil {
			continue
		}

		select {
		case l.datagrams <- UDPDatagram{FlowID: frame.FlowID, Payload: frame.Payload}:
		case <-l.ctxDone:
			return
		}
	}
}

func (l *UDPListener) runRenewLoop() {
	interval := l.leaseTTL / 2
	if interval <= 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-l.ctxDone:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(l.context(), 10*time.Second)
			err := l.client.renewLease(ctx, l.leaseID, l.reverseToken, l.leaseTTL)
			cancel()
			if err != nil {
				log.Warn().
					Err(err).
					Str("component", "sdk-udp-listener").
					Str("lease_id", l.leaseID).
					Msg("lease renewal failed")
			}
		}
	}
}

func (l *UDPListener) context() context.Context {
	if l.baseContext != nil {
		if ctx := l.baseContext(); ctx != nil {
			return ctx
		}
	}
	return context.Background()
}

func (l *UDPListener) isClosed() bool {
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

// datagramFrame mirrors portal.datagramFrame for SDK-side decode/encode.
type datagramFrame struct {
	FlowID  uint32
	Payload []byte
}

func encodeDatagram(flowID uint32, payload []byte) []byte {
	var buf [binary.MaxVarintLen32]byte
	n := binary.PutUvarint(buf[:], uint64(flowID))
	out := make([]byte, n+len(payload))
	copy(out, buf[:n])
	copy(out[n:], payload)
	return out
}

func decodeDatagram(data []byte) (datagramFrame, error) {
	flowID, n := binary.Uvarint(data)
	if n <= 0 {
		return datagramFrame{}, fmt.Errorf("datagram too small to decode")
	}
	return datagramFrame{
		FlowID:  uint32(flowID),
		Payload: data[n:],
	}, nil
}

// quicControlResponse is read from the relay after sending the control message.
type quicControlResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// openQUICSession opens a QUIC connection to the relay for datagram transport.
// quicAddr is the relay's QUIC listen address (host:port). If empty, falls back
// to the relay base URL host.
func (c *Client) openQUICSession(ctx context.Context, quicAddr, leaseID, reverseToken string) (*quic.Conn, error) {
	tlsConf := c.rawTLSConfig.Clone()
	tlsConf.NextProtos = []string{"portal-tunnel"}

	quicConf := &quic.Config{
		EnableDatagrams: true,
		KeepAlivePeriod: 15 * time.Second,
		MaxIdleTimeout:  60 * time.Second,
	}

	dialAddr := ensurePort(c.baseURL.Host)
	if quicAddr != "" {
		dialAddr = quicAddr
	}

	conn, err := quic.DialAddr(ctx, dialAddr, tlsConf, quicConf)
	if err != nil {
		return nil, fmt.Errorf("quic dial: %w", err)
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		_ = conn.CloseWithError(1, "stream open failed")
		return nil, fmt.Errorf("open control stream: %w", err)
	}

	controlMsg, _ := json.Marshal(map[string]string{
		"lease_id":      leaseID,
		"reverse_token": reverseToken,
	})
	if _, err := stream.Write(controlMsg); err != nil {
		_ = conn.CloseWithError(1, "control write failed")
		return nil, fmt.Errorf("write control: %w", err)
	}

	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	buf := make([]byte, 4096)
	n, err := stream.Read(buf)
	if err != nil {
		_ = conn.CloseWithError(1, "control read failed")
		return nil, fmt.Errorf("read control response: %w", err)
	}

	var resp quicControlResponse
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		_ = conn.CloseWithError(1, "invalid response")
		return nil, fmt.Errorf("decode control response: %w", err)
	}
	if !resp.OK {
		_ = conn.CloseWithError(1, resp.Error)
		return nil, fmt.Errorf("quic connect rejected: %s", resp.Error)
	}

	return conn, nil
}

// AttachUDP creates a UDPListener that connects to an existing lease's QUIC
// broker without registering a new lease or running a renew loop. The caller
// (typically a TCP Listener) owns the lease lifecycle.
func (c *Client) AttachUDP(ctx context.Context, leaseID, reverseToken, udpAddr, quicAddr string) (*UDPListener, error) {
	if leaseID == "" {
		return nil, errors.New("lease id is required for AttachUDP")
	}

	listenerCtx, cancel := context.WithCancel(ctx)
	listener := &UDPListener{
		client:       c,
		baseContext:  func() context.Context { return listenerCtx },
		ctxDone:      listenerCtx.Done(),
		cancel:       cancel,
		leaseID:      leaseID,
		reverseToken: reverseToken,
		udpAddr:      udpAddr,
		quicAddr:     quicAddr,
		datagrams:    make(chan UDPDatagram, 256),
		done:         make(chan struct{}),
		ownsLease:    false,
	}

	go listener.runSupervisor()
	return listener, nil
}

// ListenUDP registers a UDP-transport lease and returns a UDPListener.
func (c *Client) ListenUDP(ctx context.Context, req ListenRequest) (*UDPListener, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("listener name is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	reverseToken := strings.TrimSpace(req.ReverseToken)
	if reverseToken == "" {
		reverseToken = randomToken()
	}
	leaseTTL := req.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = c.leaseTTL
	}

	transport := strings.TrimSpace(strings.ToLower(req.Transport))
	if transport == "" {
		transport = types.TransportUDP
	}

	registerReq := types.RegisterRequest{
		Name:         req.Name,
		Hostnames:    req.Hostnames,
		Metadata:     req.Metadata,
		ReverseToken: reverseToken,
		TLS:          true,
		TTLSeconds:   int(leaseTTL / time.Second),
		Transport:    transport,
	}

	var registerResp types.RegisterResponse
	if err := c.doJSON(ctx, http.MethodPost, types.PathSDKRegister, registerReq, &registerResp); err != nil {
		return nil, err
	}

	listenerCtx, cancel := context.WithCancel(ctx)
	listener := &UDPListener{
		client:       c,
		baseContext:  func() context.Context { return listenerCtx },
		ctxDone:      listenerCtx.Done(),
		cancel:       cancel,
		name:         strings.TrimSpace(req.Name),
		leaseID:      registerResp.LeaseID,
		reverseToken: reverseToken,
		udpAddr:      registerResp.UDPAddr,
		quicAddr:     registerResp.QUICAddr,
		hostnames:    registerResp.Hostnames,
		metadata:     registerResp.Metadata,
		leaseTTL:     leaseTTL,
		datagrams:    make(chan UDPDatagram, 256),
		done:         make(chan struct{}),
		ownsLease:    true,
	}

	go listener.runSupervisor()
	go listener.runRenewLoop()

	return listener, nil
}
