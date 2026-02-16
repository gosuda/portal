// Package portal provides client-side functionality for establishing and managing
// relay connections. It handles secure communication channels, lease management,
// and connection multiplexing through the relay server.
package portal

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
)

var (
	// ErrInvalidResponse is returned when the relay server sends an unexpected or malformed response
	ErrInvalidResponse = errors.New("invalid response")
	// ErrConnectionRejected is returned when the relay server rejects a connection request
	ErrConnectionRejected = errors.New("connection rejected")
	// ErrRemoteIDMismatch is returned when the remote peer's ID doesn't match the expected lease ID
	ErrRemoteIDMismatch = errors.New("remote ID mismatch")
)

// IncomingConn represents an incoming connection from a remote client.
// It wraps a secure connection with the associated lease ID that was used
// for the connection request.
type IncomingConn struct {
	*cryptoops.SecureConnection
	leaseID string
}

// LeaseID returns the lease ID associated with this incoming connection
func (i *IncomingConn) LeaseID() string {
	return i.leaseID
}

// LocalID returns the local identity ID from the secure connection
func (i *IncomingConn) LocalID() string {
	return i.SecureConnection.LocalID()
}

// RemoteID returns the remote peer's identity ID from the secure connection
func (i *IncomingConn) RemoteID() string {
	return i.SecureConnection.RemoteID()
}

// RelayClient manages a connection to a relay server and handles:
// - Lease registration and renewal
// - Incoming connection requests
// - Secure connection establishment
//
// The client uses session multiplexing, allowing multiple
// concurrent streams over a single underlying connection.
//
// Thread-safety: All public methods are safe for concurrent use.
type RelayClient struct {
	// sess is the multiplexed session for stream management
	sess Session

	// leases maps lease IDs to their credentials for handling incoming connections
	leases   map[string]*leaseWithCred
	leasesMu sync.Mutex

	// stopClientCh signals background workers to shut down
	stopClientCh chan struct{}
	stopOnce     sync.Once // Ensure stopClientCh is closed only once
	waitGroup    sync.WaitGroup

	// incomingConnCh delivers incoming connections to the application
	incomingConnCh chan *IncomingConn
}

// leaseWithCred pairs a lease with its associated credentials.
// This is used internally to verify and sign messages for lease operations.
type leaseWithCred struct {
	Lease *rdverb.Lease
	Cred  *cryptoops.Credential
}

// NewRelayClient creates a new relay client from an established multiplexed session.
// It starts background workers for lease renewal and incoming connection handling.
//
// The client starts two goroutines:
// - leaseUpdateWorker: Periodically renews leases before they expire
// - leaseListenWorker: Accepts and handles incoming connection requests
func NewRelayClient(sess Session) *RelayClient {
	log.Debug().Msg("[RelayClient] Creating new relay client")

	g := &RelayClient{
		sess:           sess,
		leases:         make(map[string]*leaseWithCred),
		stopClientCh:   make(chan struct{}),
		incomingConnCh: make(chan *IncomingConn),
	}

	g.waitGroup.Add(2) // One for leaseUpdateWorker, one for leaseListenWorker
	go g.leaseUpdateWorker()
	go g.leaseListenWorker()

	log.Debug().Msg("[RelayClient] RelayClient initialized and workers started")
	return g
}

// Ping measures round-trip latency to the relay server.
// If the session exposes a native Ping, it is used.
// Otherwise, a stream open/close round-trip is used as a health check.
func (g *RelayClient) Ping() (time.Duration, error) {
	// Use native Ping if available
	type pinger interface {
		Ping() (time.Duration, error)
	}
	if p, ok := g.sess.(pinger); ok {
		return p.Ping()
	}

	// Fallback: open and immediately close a stream as a health check
	start := time.Now()
	stream, err := g.sess.OpenStream(context.Background())
	if err != nil {
		return 0, err
	}
	stream.Close()
	return time.Since(start), nil
}

// Close gracefully shuts down the relay client.
// It signals all background workers to stop, waits for them to finish,
// then closes the session (and its underlying transport).
// This method is safe to call multiple times.
func (g *RelayClient) Close() error {
	log.Debug().Msg("[RelayClient] Closing relay client")

	// Signal workers to stop (only once)
	g.stopOnce.Do(func() {
		close(g.stopClientCh)
	})

	// Close the session to unblock AcceptStream() calls
	var closeErr error
	if g.sess != nil {
		if err := g.sess.Close(); err != nil {
			log.Error().Err(err).Msg("[RelayClient] Error closing session")
			closeErr = err
		}
	}

	// Wait for workers to finish after unblocking them
	g.waitGroup.Wait()

	log.Debug().Msg("[RelayClient] Relay client closed")
	return closeErr
}

// leaseUpdateWorker is a background goroutine that periodically renews leases
// before they expire. It checks every 5 seconds and renews any lease that
// will expire within the next 30 seconds.
func (g *RelayClient) leaseUpdateWorker() {
	defer g.waitGroup.Done()

	ticker := time.NewTicker(5 * time.Second)
	var updateRequired = map[*leaseWithCred]struct{}{}

	defer ticker.Stop()
	for {
		select {
		case <-g.stopClientCh:
			return
		case <-ticker.C:
			// Clear the map for the next update cycle
			clear(updateRequired)

			g.leasesMu.Lock()
			for _, lease := range g.leases {
				// Check if lease expires within 30 seconds
				if lease.Lease.Expires < int64(time.Now().Add(30*time.Second).Unix()) {
					updateRequired[lease] = struct{}{}
				}
			}
			g.leasesMu.Unlock()

			for lease := range updateRequired {
				lease.Lease.Expires = time.Now().Add(30 * time.Second).Unix()
				// Check if session is available before updating lease
				if g.sess != nil {
					_, err := g.updateLease(lease.Cred, lease.Lease)
					if err != nil {
						log.Error().Err(err).Msg("[RelayClient] Failed to update lease")
					}
				}
			}
		}
	}
}

// leaseListenWorker is a background goroutine that accepts incoming connection
// requests from the relay server. It blocks on AcceptStream() and spawns a
// new goroutine to handle each connection request.
func (g *RelayClient) leaseListenWorker() {
	defer g.waitGroup.Done()
	defer close(g.incomingConnCh)
	log.Debug().Msg("[RelayClient] Lease listen worker started")

	// Create a context that cancels when the client stops
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-g.stopClientCh
		cancel()
	}()
	defer cancel()

	for {
		if g.sess == nil {
			select {
			case <-g.stopClientCh:
				log.Debug().Msg("[RelayClient] Lease listen worker stopped")
				return
			case <-time.After(500 * time.Millisecond):
				continue
			}
		}

		stream, err := g.sess.AcceptStream(ctx)
		if err != nil {
			select {
			case <-g.stopClientCh:
				log.Debug().Msg("[RelayClient] Lease listen worker stopped")
				return
			default:
				log.Debug().Err(err).Msg("[RelayClient] Error accepting stream, retrying")
				time.Sleep(500 * time.Millisecond)
				continue
			}
		}
		log.Debug().Msg("[RelayClient] Accepted incoming stream")
		go g.handleConnectionRequestStream(stream)
	}
}

// handleConnectionRequestStream processes an incoming connection request from a client.
// It performs the following steps:
// 1. Reads and validates the connection request packet
// 2. Looks up the requested lease ID
// 3. Sends an accept/reject response
// 4. If accepted, performs server-side cryptographic handshake
// 5. Sends the established secure connection to the incoming channel
func (g *RelayClient) handleConnectionRequestStream(stream Stream) {
	log.Debug().Msg("[RelayClient] Handling connection request stream")

	pkt, err := readPacket(stream)
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to read packet from stream")
		err = stream.Close()
		if err != nil {
			log.Error().Err(err).Msg("[RelayClient] Failed to close stream")
		}
		return
	}

	if pkt.Type != rdverb.PacketType_PACKET_TYPE_CONNECTION_REQUEST {
		log.Warn().Str("packet_type", pkt.Type.String()).Msg("[RelayClient] Unexpected packet type")
		err = stream.Close()
		if err != nil {
			log.Error().Err(err).Msg("[RelayClient] Failed to close stream")
		}
		return
	}

	req := &rdverb.ConnectionRequest{}
	err = req.UnmarshalVT(pkt.Payload)
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to unmarshal connection request")
		err = stream.Close()
		if err != nil {
			log.Error().Err(err).Msg("[RelayClient] Failed to close stream")
		}
		return
	}

	log.Debug().Str("lease_id", req.LeaseId).Msg("[RelayClient] Connection request received")

	g.leasesMu.Lock()
	lease, ok := g.leases[req.LeaseId]
	g.leasesMu.Unlock()

	resp := &rdverb.ConnectionResponse{}
	if !ok {
		log.Warn().Str("lease_id", req.LeaseId).Msg("[RelayClient] Lease not found, rejecting connection")
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_REJECTED
	} else {
		log.Debug().Str("lease_id", req.LeaseId).Msg("[RelayClient] Lease found, accepting connection")
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED
	}

	respPayload, err := resp.MarshalVT()
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to marshal response")
		err = stream.Close()
		if err != nil {
			log.Error().Err(err).Msg("[RelayClient] Failed to close stream")
		}
		return
	}

	err = writePacket(stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
		Payload: respPayload,
	})
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to write response packet")
		err = stream.Close()
		if err != nil {
			log.Error().Err(err).Msg("[RelayClient] Failed to close stream")
		}
		return
	}

	if !ok {
		err = stream.Close()
		if err != nil {
			log.Error().Err(err).Msg("[RelayClient] Failed to close stream")
		}
		return
	}

	log.Debug().Str("lease_id", req.LeaseId).Msg("[RelayClient] Starting server handshake")
	handshaker := cryptoops.NewHandshaker(lease.Cred)
	secConn, err := handshaker.ServerHandshake(stream, lease.Lease.Alpn)
	if err != nil {
		log.Error().Err(err).Str("lease_id", req.LeaseId).Msg("[RelayClient] Server handshake failed")
		err = stream.Close()
		if err != nil {
			log.Error().Err(err).Msg("[RelayClient] Failed to close stream")
		}
		return
	}

	log.Debug().
		Str("lease_id", req.LeaseId).
		Str("local_id", secConn.LocalID()).
		Str("remote_id", secConn.RemoteID()).
		Msg("[RelayClient] Secure connection established, sending to incoming channel")

	g.incomingConnCh <- &IncomingConn{
		SecureConnection: secConn,
		leaseID:          req.LeaseId,
	}
}

// GetRelayInfo requests relay server information including supported protocols,
// server version, and other metadata.
func (g *RelayClient) GetRelayInfo() (*rdverb.RelayInfo, error) {
	stream, err := g.sess.OpenStream(context.Background())
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	req := &rdverb.RelayInfoRequest{}
	reqPayload, err := req.MarshalVT()
	if err != nil {
		return nil, err
	}

	err = writePacket(stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_RELAY_INFO_REQUEST,
		Payload: reqPayload,
	})
	if err != nil {
		return nil, err
	}

	respPacket, err := readPacket(stream)
	if err != nil {
		return nil, err
	}

	if respPacket.Type != rdverb.PacketType_PACKET_TYPE_RELAY_INFO_RESPONSE {
		return nil, ErrInvalidResponse
	}

	var resp rdverb.RelayInfoResponse
	err = resp.UnmarshalVT(respPacket.Payload)
	if err != nil {
		return nil, err
	}

	return resp.RelayInfo, nil
}

// updateLease sends a lease update request to the relay server.
// The request is signed with the provided credentials to prove ownership.
// A nonce and timestamp are included to prevent replay attacks.
func (g *RelayClient) updateLease(cred *cryptoops.Credential, lease *rdverb.Lease) (rdverb.ResponseCode, error) {
	stream, err := g.sess.OpenStream(context.Background())
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}
	defer stream.Close()

	timestamp := time.Now().Unix()
	nonce := make([]byte, 12) // 12-byte nonce for replay protection
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	req := &rdverb.LeaseUpdateRequest{
		Lease:     lease,
		Nonce:     nonce,
		Timestamp: timestamp,
	}

	reqPayload, err := req.MarshalVT()
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	signedPayload := &rdsec.SignedPayload{
		Data:      reqPayload,
		Signature: cred.Sign(reqPayload),
	}

	signedData, err := signedPayload.MarshalVT()
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	err = writePacket(stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_LEASE_UPDATE_REQUEST,
		Payload: signedData,
	})
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	respPacket, err := readPacket(stream)
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to read packet from stream")
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	if respPacket.Type != rdverb.PacketType_PACKET_TYPE_LEASE_UPDATE_RESPONSE {
		log.Error().Msg("[RelayClient] Unexpected response packet type")
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, ErrInvalidResponse
	}

	var resp rdverb.LeaseUpdateResponse
	err = resp.UnmarshalVT(respPacket.Payload)
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to unmarshal lease update response")
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	return resp.Code, nil
}

// deleteLease sends a lease deletion request to the relay server.
// The request is signed with the provided credentials to prove ownership.
func (g *RelayClient) deleteLease(cred *cryptoops.Credential, identity *rdsec.Identity) (rdverb.ResponseCode, error) {
	stream, err := g.sess.OpenStream(context.Background())
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}
	defer stream.Close()

	timestamp := time.Now().Unix()
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	req := &rdverb.LeaseDeleteRequest{
		Identity:  identity,
		Nonce:     nonce,
		Timestamp: timestamp,
	}

	reqPayload, err := req.MarshalVT()
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	signedPayload := &rdsec.SignedPayload{
		Data:      reqPayload,
		Signature: cred.Sign(reqPayload),
	}

	signedData, err := signedPayload.MarshalVT()
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	// Send deletion request
	err = writePacket(stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_LEASE_DELETE_REQUEST,
		Payload: signedData,
	})
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	// Receive deletion response
	respPacket, err := readPacket(stream)
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to read packet from stream")
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	if respPacket.Type != rdverb.PacketType_PACKET_TYPE_LEASE_DELETE_RESPONSE {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, ErrInvalidResponse
	}

	var resp rdverb.LeaseDeleteResponse
	err = resp.UnmarshalVT(respPacket.Payload)
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	return resp.Code, nil
}

// RequestConnection initiates a connection to a remote peer through the relay.
// It performs the following steps:
// 1. Opens a new stream to the relay server
// 2. Sends a connection request for the specified lease ID
// 3. Waits for accept/reject response from the remote peer
// 4. If accepted, performs client-side cryptographic handshake
// 5. Verifies the remote peer's ID matches the lease ID
//
// Parameters:
//   - leaseID: The ID of the lease to connect to
//   - alpn: Application-Layer Protocol Negotiation string
//   - clientCred: Client's cryptographic credentials for the handshake
//
// Returns the response code, established secure connection (if successful), and any error.
func (g *RelayClient) RequestConnection(leaseID string, alpn string, clientCred *cryptoops.Credential) (rdverb.ResponseCode, *cryptoops.SecureConnection, error) {
	log.Debug().Str("lease_id", leaseID).Str("alpn", alpn).Msg("[RelayClient] Requesting connection")

	stream, err := g.sess.OpenStream(context.Background())
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to open stream for connection request")
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	clientIdentity := &rdsec.Identity{
		Id:        clientCred.ID(),
		PublicKey: clientCred.PublicKey(),
	}

	req := &rdverb.ConnectionRequest{
		LeaseId:        leaseID,
		ClientIdentity: clientIdentity,
	}

	reqPayload, err := req.MarshalVT()
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to marshal connection request")
		err = stream.Close()
		if err != nil {
			log.Error().Err(err).Msg("[RelayClient] Failed to close stream")
		}
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	log.Debug().Str("lease_id", leaseID).Msg("[RelayClient] Sending connection request")
	err = writePacket(stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_REQUEST,
		Payload: reqPayload,
	})
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to write connection request packet")
		err = stream.Close()
		if err != nil {
			log.Error().Err(err).Msg("[RelayClient] Failed to close stream")
		}
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	log.Debug().Str("lease_id", leaseID).Msg("[RelayClient] Waiting for connection response")
	respPacket, err := readPacket(stream)
	if err != nil {
		log.Error().Str("lease_id", leaseID).Err(err).Msg("[RelayClient] Failed to read connection response")
		err = stream.Close()
		if err != nil {
			log.Error().Err(err).Msg("[RelayClient] Failed to close stream")
		}
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	if respPacket.Type != rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE {
		log.Warn().Str("packet_type", respPacket.Type.String()).Msg("[RelayClient] Unexpected response packet type")
		err = stream.Close()
		if err != nil {
			log.Error().Err(err).Msg("[RelayClient] Failed to close stream")
		}
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, ErrInvalidResponse
	}

	var resp rdverb.ConnectionResponse
	err = resp.UnmarshalVT(respPacket.Payload)
	if err != nil {
		log.Error().Str("lease_id", leaseID).Err(err).Msg("[RelayClient] Failed to unmarshal connection response")
		err = stream.Close()
		if err != nil {
			log.Error().Err(err).Msg("[RelayClient] Failed to close stream")
		}
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	log.Debug().
		Str("lease_id", leaseID).
		Str("response_code", resp.Code.String()).
		Msg("[RelayClient] Connection response received")

	if resp.Code != rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
		log.Warn().Str("lease_id", leaseID).Str("code", resp.Code.String()).Msg("[RelayClient] Connection rejected")
		stream.Close()
		return resp.Code, nil, ErrConnectionRejected
	}

	log.Debug().Str("lease_id", leaseID).Msg("[RelayClient] Starting client handshake")
	handshaker := cryptoops.NewHandshaker(clientCred)
	secConn, err := handshaker.ClientHandshake(stream, alpn)
	if err != nil {
		log.Error().Err(err).Str("lease_id", leaseID).Msg("[RelayClient] Client handshake failed")
		stream.Close()
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	// Verify the remote peer's ID matches the expected lease ID
	if secConn.RemoteID() != leaseID {
		log.Warn().Str("lease_id", leaseID).Msg("[RelayClient] Remote ID mismatch")
		stream.Close()
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, ErrRemoteIDMismatch
	}

	log.Debug().
		Str("lease_id", leaseID).
		Str("local_id", secConn.LocalID()).
		Str("remote_id", secConn.RemoteID()).
		Msg("[RelayClient] Secure connection established successfully")

	return resp.Code, secConn, nil
}

// RegisterLease registers a new lease with the relay server.
// The lease allows remote clients to connect to this client via the relay.
//
// The lease is cloned to avoid modifying the caller's original lease object.
// On registration failure, the lease is automatically removed from the local cache.
func (g *RelayClient) RegisterLease(cred *cryptoops.Credential, lease *rdverb.Lease) error {
	lease = lease.CloneVT() // Clone to avoid modifying the original lease

	identity := &rdsec.Identity{
		Id:        cred.ID(),
		PublicKey: cred.PublicKey(),
	}
	lease.Identity = identity
	lease.Expires = time.Now().Add(30 * time.Second).Unix()

	log.Debug().
		Str("lease_id", identity.Id).
		Str("name", lease.Name).
		Strs("alpns", lease.Alpn).
		Msg("[RelayClient] Registering lease")

	g.leasesMu.Lock()
	g.leases[identity.Id] = &leaseWithCred{
		Lease: lease,
		Cred:  cred,
	}
	g.leasesMu.Unlock()

	resp, err := g.updateLease(cred, lease)
	if err != nil || resp != rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
		log.Error().
			Err(err).
			Str("lease_id", identity.Id).
			Str("response", resp.String()).
			Msg("[RelayClient] Failed to register lease")
		g.leasesMu.Lock()
		delete(g.leases, identity.Id)
		g.leasesMu.Unlock()
		return err
	}

	log.Debug().Str("lease_id", identity.Id).Msg("[RelayClient] Lease registered successfully")
	return nil
}

// DeregisterLease removes a lease from the relay server.
// It removes the lease from the local cache immediately, then notifies the server.
func (g *RelayClient) DeregisterLease(cred *cryptoops.Credential) error {
	identity := &rdsec.Identity{
		Id:        cred.ID(),
		PublicKey: cred.PublicKey(),
	}

	g.leasesMu.Lock()
	delete(g.leases, identity.Id)
	g.leasesMu.Unlock()

	resp, err := g.deleteLease(cred, identity)
	if err != nil || resp != rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
		log.Error().Err(err).Str("lease_id", identity.Id).Msg("[RelayClient] Failed to deregister lease")
		return err
	}

	log.Debug().Str("lease_id", identity.Id).Msg("[RelayClient] Lease unregistered successfully")
	return nil
}

// IncomingConnection returns a receive-only channel for incoming connections.
// The channel is closed when the relay client is shut down.
func (g *RelayClient) IncomingConnection() <-chan *IncomingConn {
	return g.incomingConnCh
}
