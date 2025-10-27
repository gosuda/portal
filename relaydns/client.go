package relaydns

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/gosuda/relaydns/relaydns/core/cryptoops"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdsec"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdverb"
	"github.com/hashicorp/yamux"
)

var (
	ErrInvalidResponse    = errors.New("invalid response")
	ErrConnectionRejected = errors.New("connection rejected")
)

type IncommingConn struct {
	*cryptoops.SecureConnection
	id string
}

func (i *IncommingConn) ID() string {
	return i.id
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
	g := &RelayClient{
		conn:            conn,
		leases:          make(map[string]*leaseWithCred),
		stopCh:          make(chan struct{}),
		incommingConnCh: make(chan *IncommingConn),
	}

	g.waitGroup.Add(1)
	go g.leaseUpdateWorker()
	go g.leaseListenWorker()

	return g
}

// Close는 서버와의 연결을 종료합니다.
func (g *RelayClient) Close() error {
	close(g.stopCh)
	g.waitGroup.Wait()

	err := g.conn.Close()
	if err != nil {
		return err
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
				g.updateLease(context.Background(), lease.Cred, lease.Lease)
			}
		}
	}
}

func (g *RelayClient) leaseListenWorker() {
	for {
		stream, err := g.sess.AcceptStream()
		if err != nil {
			continue
		}
		go g.handleConnectionRequestStream(stream)
	}
}

func (g *RelayClient) handleConnectionRequestStream(stream *yamux.Stream) {
	pkt, err := readPacket(stream)
	if err != nil {
		stream.Close()
		return
	}

	if pkt.Type != rdverb.PacketType_PACKET_TYPE_CONNECTION_REQUEST {
		stream.Close()
		return
	}

	req := &rdverb.ConnectionRequest{}
	err = req.UnmarshalVT(pkt.Payload)
	if err != nil {
		stream.Close()
		return
	}

	g.leasesMu.Lock()
	lease, ok := g.leases[req.LeaseId]
	g.leasesMu.Unlock()

	resp := &rdverb.ConnectionResponse{}
	if !ok {
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_REJECTED
	} else {
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED
	}

	respPayload, err := resp.MarshalVT()
	if err != nil {
		stream.Close()
		return
	}

	err = writePacket(stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
		Payload: respPayload,
	})
	if err != nil {
		stream.Close()
		return
	}

	if !ok {
		stream.Close()
		return
	}

	handshaker := cryptoops.NewHandshaker(lease.Cred)
	secConn, err := handshaker.ServerHandshake(stream, lease.Lease.Alpn)
	if err != nil {
		stream.Close()
		return
	}

	g.incommingConnCh <- &IncommingConn{
		SecureConnection: secConn,
		id:               req.LeaseId,
	}
}

// GetRelayInfo는 서버의 릴레이 정보를 요청합니다.
func (g *RelayClient) GetRelayInfo(ctx context.Context) (*rdverb.RelayInfo, error) {
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
func (g *RelayClient) updateLease(ctx context.Context, cred *cryptoops.Credential, lease *rdverb.Lease) (rdverb.ResponseCode, error) {
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
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	if respPacket.Type != rdverb.PacketType_PACKET_TYPE_LEASE_UPDATE_RESPONSE {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, ErrInvalidResponse
	}

	var resp rdverb.LeaseUpdateResponse
	err = resp.UnmarshalVT(respPacket.Payload)
	if err != nil {
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, err
	}

	return resp.Code, nil
}

// deleteLease는 서버에 리스 삭제를 요청합니다.
func (g *RelayClient) deleteLease(ctx context.Context, cred *cryptoops.Credential, identity *rdsec.Identity) (rdverb.ResponseCode, error) {
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
func (g *RelayClient) requestConnection(ctx context.Context, leaseID string, alpn string, clientCred *cryptoops.Credential) (rdverb.ResponseCode, io.ReadWriteCloser, error) {
	// 새 스트림 열기
	stream, err := g.sess.OpenStream()
	if err != nil {
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
		stream.Close()
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	// 요청 전송
	err = writePacket(stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_REQUEST,
		Payload: reqPayload,
	})
	if err != nil {
		stream.Close()
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	// 응답 수신
	respPacket, err := readPacket(stream)
	if err != nil {
		stream.Close()
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	if respPacket.Type != rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE {
		stream.Close()
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, ErrInvalidResponse
	}

	var resp rdverb.ConnectionResponse
	err = resp.UnmarshalVT(respPacket.Payload)
	if err != nil {
		stream.Close()
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	// 거절된 경우 스트림을 닫고 오류 코드 반환
	if resp.Code != rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
		stream.Close()
		return resp.Code, nil, ErrConnectionRejected
	}

	handshaker := cryptoops.NewHandshaker(clientCred)
	secConn, err := handshaker.ClientHandshake(stream, alpn)
	if err != nil {
		stream.Close()
		return rdverb.ResponseCode_RESPONSE_CODE_UNKNOWN, nil, err
	}

	return resp.Code, secConn, nil
}

func (g *RelayClient) RegisterLease(ctx context.Context, cred *cryptoops.Credential, name string, alpns []string) error {
	identity := &rdsec.Identity{
		Id:        cred.ID(),
		PublicKey: cred.PublicKey(),
	}

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

	resp, err := g.updateLease(ctx, cred, lease)
	if err != nil || resp != rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
		g.leasesMu.Lock()
		delete(g.leases, identity.Id)
		g.leasesMu.Unlock()
		return err
	}

	return nil
}

func (g *RelayClient) DeregisterLease(ctx context.Context, cred *cryptoops.Credential) error {
	identity := &rdsec.Identity{
		Id:        cred.ID(),
		PublicKey: cred.PublicKey(),
	}

	resp, err := g.deleteLease(ctx, cred, identity)
	if err != nil || resp != rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
		return err
	}

	g.leasesMu.Lock()
	delete(g.leases, identity.Id)
	g.leasesMu.Unlock()

	return nil
}

func (g *RelayClient) IncommingConnection() <-chan *IncommingConn {
	return g.incommingConnCh
}
