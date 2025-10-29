package relaydns

import (
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/gorilla/websocket"
	"github.com/gosuda/relaydns/relaydns/core/cryptoops"
	"github.com/gosuda/relaydns/relaydns/utils/wsstream"
	"github.com/rs/zerolog/log"
)

var (
	ErrInvalidURL      = errors.New("invalid WebSocket URL")
	ErrUnsupportedALPN = errors.New("unsupported ALPN protocol")
)

// SecureWebSocketClient provides E2EE WebSocket connections through RelayDNS
type SecureWebSocketClient struct {
	relayClient *RelayClient
	credential  *cryptoops.Credential
}

// NewSecureWebSocketClient creates a new E2EE WebSocket client
//
// Parameters:
//   - relayConn: Connection to relay server (usually ws://relay-server/relay)
//   - credential: Client credential for E2EE authentication
//
// Example:
//
//	// Connect to relay server
//	relayConn, _, err := websocket.DefaultDialer.Dial("ws://localhost:4017/relay", nil)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Create credential
//	cred, err := cryptoops.NewRandomCredential()
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	// Create E2EE WebSocket client
//	client := NewSecureWebSocketClient(relayConn, cred)
func NewSecureWebSocketClient(relayConn io.ReadWriteCloser, credential *cryptoops.Credential) *SecureWebSocketClient {
	relayClient := NewRelayClient(relayConn)

	return &SecureWebSocketClient{
		relayClient: relayClient,
		credential:  credential,
	}
}

// Dial establishes an E2EE WebSocket connection to a target peer
//
// The target URL should use the "peer://" scheme:
//   - peer://peer-id/path - Connect to peer via E2EE tunnel
//
// Parameters:
//   - targetURL: Target WebSocket URL (peer://peer-id/path)
//   - alpn: Application-Layer Protocol Negotiation (e.g., "websocket", "http")
//
// Returns:
//   - *SecureWebSocketConn: Established E2EE connection
//   - error: Error if connection fails
//
// Example:
//
//	conn, err := client.Dial("peer://abc123/chat", "websocket")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer conn.Close()
//
//	// Send message
//	err = conn.WriteMessage(websocket.TextMessage, []byte("Hello!"))
//
//	// Receive message
//	messageType, data, err := conn.ReadMessage()
func (c *SecureWebSocketClient) Dial(targetURL, alpn string) (*SecureWebSocketConn, error) {
	log.Debug().
		Str("target_url", targetURL).
		Str("alpn", alpn).
		Msg("[SecureWebSocket] Dialing E2EE WebSocket")

	// Parse target URL
	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	// Extract peer ID from URL
	// peer://peer-id/path -> peer-id is the lease ID
	peerID := parsedURL.Host
	if peerID == "" {
		return nil, fmt.Errorf("%w: missing peer ID", ErrInvalidURL)
	}

	log.Debug().
		Str("peer_id", peerID).
		Str("alpn", alpn).
		Msg("[SecureWebSocket] Requesting connection to peer")

	// Request connection through relay
	code, secConn, err := c.relayClient.RequestConnection(peerID, alpn, c.credential)
	if err != nil {
		return nil, fmt.Errorf("connection request failed: %w", err)
	}

	if secConn == nil {
		return nil, fmt.Errorf("connection rejected with code: %v", code)
	}

	log.Debug().
		Str("peer_id", peerID).
		Str("local_id", secConn.LocalID()).
		Str("remote_id", secConn.RemoteID()).
		Msg("[SecureWebSocket] E2EE connection established")

	return &SecureWebSocketConn{
		conn:     secConn,
		peerID:   peerID,
		alpn:     alpn,
		isClosed: false,
	}, nil
}

// RegisterService registers this client as a WebSocket service provider
//
// After registration, other clients can connect to this service using:
//   - peer://your-peer-id/path
//
// Parameters:
//   - name: Service name (for human readability)
//   - alpns: Supported ALPN protocols (e.g., []string{"websocket", "http"})
//
// Returns incoming connections via IncomingConnections() channel
//
// Example:
//
//	err := client.RegisterService("chat-server", []string{"websocket"})
//	if err != nil {
//	    log.Fatal(err)
//	}
//
//	log.Printf("Service registered with ID: %s", client.PeerID())
//
//	for conn := range client.IncomingConnections() {
//	    go handleConnection(conn)
//	}
func (c *SecureWebSocketClient) RegisterService(name string, alpns []string) error {
	log.Debug().
		Str("name", name).
		Strs("alpns", alpns).
		Msg("[SecureWebSocket] Registering service")

	err := c.relayClient.RegisterLease(c.credential, name, alpns)
	if err != nil {
		return fmt.Errorf("failed to register service: %w", err)
	}

	log.Info().
		Str("peer_id", c.credential.ID()).
		Str("name", name).
		Strs("alpns", alpns).
		Msg("[SecureWebSocket] Service registered successfully")

	return nil
}

// DeregisterService removes this client from the relay server
func (c *SecureWebSocketClient) DeregisterService() error {
	log.Debug().Msg("[SecureWebSocket] Deregistering service")

	err := c.relayClient.DeregisterLease(c.credential)
	if err != nil {
		return fmt.Errorf("failed to deregister service: %w", err)
	}

	log.Info().Str("peer_id", c.credential.ID()).Msg("[SecureWebSocket] Service deregistered")
	return nil
}

// IncomingConnections returns a channel that receives incoming E2EE connections
//
// This channel delivers connections from other peers connecting to this service.
// Each connection is already authenticated and encrypted.
//
// Example:
//
//	for conn := range client.IncomingConnections() {
//	    log.Printf("New connection from: %s", conn.RemoteID())
//	    go handleConnection(conn)
//	}
func (c *SecureWebSocketClient) IncomingConnections() <-chan *SecureWebSocketConn {
	ch := make(chan *SecureWebSocketConn)

	go func() {
		for incomingConn := range c.relayClient.IncommingConnection() {
			log.Debug().
				Str("lease_id", incomingConn.LeaseID()).
				Str("remote_id", incomingConn.RemoteID()).
				Msg("[SecureWebSocket] Incoming E2EE connection")

			ch <- &SecureWebSocketConn{
				conn:     incomingConn.SecureConnection,
				peerID:   incomingConn.RemoteID(),
				alpn:     "", // ALPN is already negotiated
				isClosed: false,
			}
		}
		close(ch)
	}()

	return ch
}

// PeerID returns this client's peer ID
//
// This is the identifier that other clients use to connect to this service.
// Example: peer://your-peer-id/path
func (c *SecureWebSocketClient) PeerID() string {
	return c.credential.ID()
}

// Close closes the relay connection and stops the client
func (c *SecureWebSocketClient) Close() error {
	log.Debug().Msg("[SecureWebSocket] Closing client")
	return c.relayClient.Close()
}

// SecureWebSocketConn represents an E2EE WebSocket connection
type SecureWebSocketConn struct {
	conn     *cryptoops.SecureConnection
	peerID   string
	alpn     string
	isClosed bool
}

// ReadMessage reads the next message from the connection
//
// Compatible with gorilla/websocket ReadMessage interface.
// Always returns BinaryMessage type (E2EE encrypts everything as binary).
//
// Returns:
//   - messageType: Always websocket.BinaryMessage for E2EE
//   - data: Decrypted message data
//   - error: Error if read fails
func (c *SecureWebSocketConn) ReadMessage() (messageType int, data []byte, err error) {
	if c.isClosed {
		return 0, nil, io.EOF
	}

	// Read encrypted data
	buf := make([]byte, 65536) // 64KB buffer
	n, err := c.conn.Read(buf)
	if err != nil {
		if err == io.EOF {
			c.isClosed = true
		}
		return 0, nil, err
	}

	// Return decrypted data as binary message
	return websocket.BinaryMessage, buf[:n], nil
}

// WriteMessage writes a message to the connection
//
// Compatible with gorilla/websocket WriteMessage interface.
// Message type is ignored as E2EE encrypts everything as binary.
//
// Parameters:
//   - messageType: Ignored (can be TextMessage or BinaryMessage)
//   - data: Message data to send (will be encrypted)
//
// Returns:
//   - error: Error if write fails
func (c *SecureWebSocketConn) WriteMessage(messageType int, data []byte) error {
	if c.isClosed {
		return io.ErrClosedPipe
	}

	// Write encrypted data
	_, err := c.conn.Write(data)
	return err
}

// Read implements io.Reader
func (c *SecureWebSocketConn) Read(p []byte) (n int, err error) {
	if c.isClosed {
		return 0, io.EOF
	}
	return c.conn.Read(p)
}

// Write implements io.Writer
func (c *SecureWebSocketConn) Write(p []byte) (n int, err error) {
	if c.isClosed {
		return 0, io.ErrClosedPipe
	}
	return c.conn.Write(p)
}

// Close closes the connection
func (c *SecureWebSocketConn) Close() error {
	if c.isClosed {
		return nil
	}

	c.isClosed = true
	return c.conn.Close()
}

// RemoteID returns the peer's ID (authenticated identity)
func (c *SecureWebSocketConn) RemoteID() string {
	return c.conn.RemoteID()
}

// LocalID returns this client's ID
func (c *SecureWebSocketConn) LocalID() string {
	return c.conn.LocalID()
}

// PeerID returns the target peer ID
func (c *SecureWebSocketConn) PeerID() string {
	return c.peerID
}

// ALPN returns the negotiated application-layer protocol
func (c *SecureWebSocketConn) ALPN() string {
	return c.alpn
}

// DialWebSocketSecure is a convenience function to establish E2EE WebSocket through relay
//
// This is a high-level helper that:
//  1. Connects to relay server
//  2. Creates E2EE client
//  3. Dials target peer
//
// Parameters:
//   - relayURL: Relay server WebSocket URL (e.g., "ws://localhost:4017/relay")
//   - targetPeerID: Target peer ID to connect to
//   - alpn: Application protocol (e.g., "websocket")
//   - credential: Client credential (or nil to generate random)
//
// Returns:
//   - *SecureWebSocketConn: Established E2EE connection
//   - *SecureWebSocketClient: Client instance (keep alive for connection)
//   - error: Error if connection fails
//
// Example:
//
//	conn, client, err := DialWebSocketSecure(
//	    "ws://localhost:4017/relay",
//	    "target-peer-id",
//	    "websocket",
//	    nil, // auto-generate credential
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer conn.Close()
//	defer client.Close()
//
//	conn.WriteMessage(websocket.TextMessage, []byte("Hello!"))
func DialWebSocketSecure(relayURL, targetPeerID, alpn string, credential *cryptoops.Credential) (*SecureWebSocketConn, *SecureWebSocketClient, error) {
	// Generate credential if not provided
	if credential == nil {
		var err error
		credential, err = cryptoops.NewCredential()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to generate credential: %w", err)
		}
	}

	// Connect to relay server
	relayConn, _, err := websocket.DefaultDialer.Dial(relayURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to relay: %w", err)
	}

	// Wrap WebSocket connection
	relayStream := &wsstream.WsStream{Conn: relayConn}

	// Create E2EE client
	client := NewSecureWebSocketClient(relayStream, credential)

	// Dial target peer
	targetURL := fmt.Sprintf("peer://%s/", targetPeerID)
	conn, err := client.Dial(targetURL, alpn)
	if err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("failed to dial peer: %w", err)
	}

	return conn, client, nil
}
