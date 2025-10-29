package relaydns

import (
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/gosuda/relaydns/relaydns/core/cryptoops"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdsec"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdverb"
	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"
)

var (
	ErrInvalidResponse    = errors.New("invalid response")
	ErrConnectionRejected = errors.New("connection rejected")
)

type IncommingConn struct {
	*cryptoops.SecureConnection
	leaseID string
}

func (i *IncommingConn) LeaseID() string {
	return i.leaseID
}

func (i *IncommingConn) LocalID() string {
	return i.SecureConnection.LocalID()
}

func (i *IncommingConn) RemoteID() string {
	return i.SecureConnection.RemoteID()
}

// RelayClient는 RelayServer에 연결하여 서비스를 요청하는 클라이언트입니다.
type RelayClient struct {
	conn io.ReadWriteCloser

	sess *yamux.Session

	leases   map[string]*leaseWithCred
	leasesMu sync.Mutex

	stopCh    chan struct{}
	waitGroup sync.WaitGroup

	incommingConnCh chan *IncommingConn
}

type leaseWithCred struct {
	Lease *rdverb.Lease
	Cred  *cryptoops.Credential
}

// NewRelayClient는 새로운 RelayClient 인스턴스를 생성합니다.
func NewRelayClient(conn io.ReadWriteCloser) *RelayClient {
	log.Debug().Msg("[RelayClient] Creating new relay client")

	// Create yamux session as client
	config := yamux.DefaultConfig()
	config.Logger = nil // Disable logging for cleaner output
	sess, err := yamux.Client(conn, config)
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to create yamux session")
		// If session creation fails, close the connection and return nil
		conn.Close()
		return nil
	}

	log.Debug().Msg("[RelayClient] Yamux session created successfully")

	g := &RelayClient{
		conn:            conn,
		sess:            sess,
		leases:          make(map[string]*leaseWithCred),
		stopCh:          make(chan struct{}),
		incommingConnCh: make(chan *IncommingConn),
	}

	g.waitGroup.Add(2) // One for leaseUpdateWorker, one for leaseListenWorker
	go g.leaseUpdateWorker()
	go g.leaseListenWorker()

	log.Debug().Msg("[RelayClient] RelayClient initialized and workers started")
	return g
}

// Close는 서버와의 연결을 종료합니다.
func (g *RelayClient) Close() error {
	log.Debug().Msg("[RelayClient] Closing relay client")

	// Signal workers to stop
	close(g.stopCh)

	var errs []error

	// Close the session first to unblock AcceptStream() calls
	if g.sess != nil {
		if err := g.sess.Close(); err != nil {
			log.Error().Err(err).Msg("[RelayClient] Error closing yamux session")
			errs = append(errs, err)
		}
	}

	// Wait for workers to finish after unblocking them
	g.waitGroup.Wait()

	// Then close the underlying connection
	if g.conn != nil {
		if err := g.conn.Close(); err != nil {
			log.Error().Err(err).Msg("[RelayClient] Error closing connection")
			errs = append(errs, err)
		}
	}

	log.Debug().Msg("[RelayClient] Relay client closed")
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// leaseUpdateWorker는 리스 업데이트를 처리하는 워커입니다.
func (g *RelayClient) leaseUpdateWorker() {
	defer g.waitGroup.Done()

	ticker := time.NewTicker(5 * time.Second)
	var updateRequired = map[*leaseWithCred]struct{}{}

	defer ticker.Stop()
	for {
		select {
		case <-g.stopCh:
			return
		case <-ticker.C:
			clear(updateRequired)

			g.leasesMu.Lock()
			for _, lease := range g.leases {
				if lease.Lease.Expires < int64(time.Now().Add(30*time.Second).Unix()) {
					updateRequired[lease] = struct{}{}
				}
			}
			g.leasesMu.Unlock()

			for lease := range updateRequired {
				lease.Lease.Expires = time.Now().Add(30 * time.Second).Unix()
				// Check if session is available before updating lease
				if g.sess != nil {
					g.updateLease(lease.Cred, lease.Lease)
				}
			}
		}
	}
}

func (g *RelayClient) leaseListenWorker() {
	defer g.waitGroup.Done()
	log.Debug().Msg("[RelayClient] Lease listen worker started")

	for {
		select {
		case <-g.stopCh:
			log.Debug().Msg("[RelayClient] Lease listen worker stopped")
			return
		default:
			if g.sess == nil {
				// Session not initialized, wait a bit and retry
				time.Sleep(500 * time.Millisecond)
				continue
			}

			stream, err := g.sess.AcceptStream()
			if err != nil {
				// Check if we're supposed to stop
				select {
				case <-g.stopCh:
					return
				default:
					log.Debug().Err(err).Msg("[RelayClient] Error accepting stream, retrying")
					time.Sleep(500 * time.Millisecond) // waiting for reconnection
				}
			}
			log.Debug().Uint32("stream_id", stream.StreamID()).Msg("[RelayClient] Accepted incoming stream")
			go g.handleConnectionRequestStream(stream)
		}
	}
}

func (g *RelayClient) handleConnectionRequestStream(stream *yamux.Stream) {
	log.Debug().Uint32("stream_id", stream.StreamID()).Msg("[RelayClient] Handling connection request stream")

	pkt, err := readPacket(stream)
	if err != nil {
		log.Error().Uint32("stream_id", stream.StreamID()).Err(err).Msg("[RelayClient] Failed to read packet from stream")
		stream.Close()
		return
	}

	if pkt.Type != rdverb.PacketType_PACKET_TYPE_CONNECTION_REQUEST {
		log.Warn().Str("packet_type", pkt.Type.String()).Msg("[RelayClient] Unexpected packet type")
		stream.Close()
		return
	}

	req := &rdverb.ConnectionRequest{}
	err = req.UnmarshalVT(pkt.Payload)
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to unmarshal connection request")
		stream.Close()
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
		stream.Close()
		return
	}

	err = writePacket(stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
		Payload: respPayload,
	})
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to write response packet")
		stream.Close()
		return
	}

	if !ok {
		stream.Close()
		return
	}

	log.Debug().Str("lease_id", req.LeaseId).Msg("[RelayClient] Starting server handshake")
	handshaker := cryptoops.NewHandshaker(lease.Cred)
	secConn, err := handshaker.ServerHandshake(stream, lease.Lease.Alpn)
	if err != nil {
		log.Error().Err(err).Str("lease_id", req.LeaseId).Msg("[RelayClient] Server handshake failed")
		stream.Close()
		return
	}

	log.Debug().
		Str("lease_id", req.LeaseId).
		Str("local_id", secConn.LocalID()).
		Str("remote_id", secConn.RemoteID()).
		Msg("[RelayClient] Secure connection established, sending to incoming channel")

	g.incommingConnCh <- &IncommingConn{
		SecureConnection: secConn,
		leaseID:          req.LeaseId,
	}
}

// GetRelayInfo는 서버의 릴레이 정보를 요청합니다.
func (g *RelayClient) GetRelayInfo() (*rdverb.RelayInfo, error) {
	// 새 스트림 열기
	stream, err := g.sess.OpenStream()
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	// 요청 패킷 생성
	req := &rdverb.RelayInfoRequest{}
	reqPayload, err := req.MarshalVT()
	if err != nil {
		return nil, err
	}

	// 요청 전송
	err = writePacket(stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_RELAY_INFO_REQUEST,
		Payload: reqPayload,
	})
	if err != nil {
		return nil, err
	}

	// 응답 수신
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

// updateLease는 서버에 리스 업데이트를 요청합니다.
func (g *RelayClient) updateLease(cred *cryptoops.Credential, lease *rdverb.Lease) (rdverb.ResponseCode, error) {
	// 새 스트림 열기
	stream, err := g.sess.OpenStream()
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}
	defer stream.Close()

	// 요청 생성
	timestamp := time.Now().Unix()
	nonce := make([]byte, 12) // 12바이트 nonce
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	req := &rdverb.LeaseUpdateRequest{
		Lease:     lease,
		Nonce:     nonce,
		Timestamp: timestamp,
	}

	// 요청 직렬화 및 서명
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

	// 요청 전송
	err = writePacket(stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_LEASE_UPDATE_REQUEST,
		Payload: signedData,
	})
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	// 응답 수신
	respPacket, err := readPacket(stream)
	if err != nil {
		log.Error().Uint32("stream_id", stream.StreamID()).Err(err).Msg("[RelayClient] Failed to read packet from stream")
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	if respPacket.Type != rdverb.PacketType_PACKET_TYPE_LEASE_UPDATE_RESPONSE {
		log.Error().Uint32("stream_id", stream.StreamID()).Msg("[RelayClient] Unexpected response packet type")
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, ErrInvalidResponse
	}

	var resp rdverb.LeaseUpdateResponse
	err = resp.UnmarshalVT(respPacket.Payload)
	if err != nil {
		log.Error().Uint32("stream_id", stream.StreamID()).Err(err).Msg("[RelayClient] Failed to unmarshal lease update response")
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	return resp.Code, nil
}

// deleteLease는 서버에 리스 삭제를 요청합니다.
func (g *RelayClient) deleteLease(cred *cryptoops.Credential, identity *rdsec.Identity) (rdverb.ResponseCode, error) {
	// 새 스트림 열기
	stream, err := g.sess.OpenStream()
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}
	defer stream.Close()

	// 요청 생성
	timestamp := time.Now().Unix()
	nonce := make([]byte, 12) // 12바이트 nonce
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	req := &rdverb.LeaseDeleteRequest{
		Identity:  identity,
		Nonce:     nonce,
		Timestamp: timestamp,
	}

	// 요청 직렬화 및 서명
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

	// 요청 전송
	err = writePacket(stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_LEASE_DELETE_REQUEST,
		Payload: signedData,
	})
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	// 응답 수신
	respPacket, err := readPacket(stream)
	if err != nil {
		log.Error().Uint32("stream_id", stream.StreamID()).Err(err).Msg("[RelayClient] Failed to read packet from stream")
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

// requestConnection은 다른 클라이언트로의 연결을 요청합니다.
func (g *RelayClient) RequestConnection(leaseID string, alpn string, clientCred *cryptoops.Credential) (rdverb.ResponseCode, *cryptoops.SecureConnection, error) {
	log.Debug().Str("lease_id", leaseID).Str("alpn", alpn).Msg("[RelayClient] Requesting connection")

	// 새 스트림 열기
	stream, err := g.sess.OpenStream()
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to open stream for connection request")
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	clientIdentity := &rdsec.Identity{
		Id:        clientCred.ID(),
		PublicKey: clientCred.PublicKey(),
	}

	// 요청 생성
	req := &rdverb.ConnectionRequest{
		LeaseId:        leaseID,
		ClientIdentity: clientIdentity,
	}

	reqPayload, err := req.MarshalVT()
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to marshal connection request")
		stream.Close()
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	// 요청 전송
	log.Debug().Str("lease_id", leaseID).Msg("[RelayClient] Sending connection request")
	err = writePacket(stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_REQUEST,
		Payload: reqPayload,
	})
	if err != nil {
		log.Error().Err(err).Msg("[RelayClient] Failed to write connection request packet")
		stream.Close()
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	// 응답 수신
	log.Debug().Str("lease_id", leaseID).Msg("[RelayClient] Waiting for connection response")
	respPacket, err := readPacket(stream)
	if err != nil {
		log.Error().Str("lease_id", leaseID).Err(err).Msg("[RelayClient] Failed to read connection response")
		stream.Close()
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	if respPacket.Type != rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE {
		log.Warn().Str("packet_type", respPacket.Type.String()).Msg("[RelayClient] Unexpected response packet type")
		stream.Close()
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, ErrInvalidResponse
	}

	var resp rdverb.ConnectionResponse
	err = resp.UnmarshalVT(respPacket.Payload)
	if err != nil {
		log.Error().Str("lease_id", leaseID).Err(err).Msg("[RelayClient] Failed to unmarshal connection response")
		stream.Close()
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	log.Debug().
		Str("lease_id", leaseID).
		Str("response_code", resp.Code.String()).
		Msg("[RelayClient] Connection response received")

	// 거절된 경우 스트림을 닫고 오류 코드 반환
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

	log.Debug().
		Str("lease_id", leaseID).
		Str("local_id", secConn.LocalID()).
		Str("remote_id", secConn.RemoteID()).
		Msg("[RelayClient] Secure connection established successfully")

	return resp.Code, secConn, nil
}

func (g *RelayClient) RegisterLease(cred *cryptoops.Credential, name string, alpns []string) error {
	identity := &rdsec.Identity{
		Id:        cred.ID(),
		PublicKey: cred.PublicKey(),
	}

	log.Debug().
		Str("lease_id", identity.Id).
		Str("name", name).
		Strs("alpns", alpns).
		Msg("[RelayClient] Registering lease")

	lease := &rdverb.Lease{
		Identity: identity,
		Expires:  time.Now().Add(30 * time.Second).Unix(),
		Name:     name,
		Alpn:     alpns,
	}

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
		return err
	}

	return nil
}

func (g *RelayClient) IncommingConnection() <-chan *IncommingConn {
	return g.incommingConnCh
}
