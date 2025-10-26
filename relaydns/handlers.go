package relaydns

import (
	"encoding/binary"
	"io"
	"sync"

	"github.com/gosuda/relaydns/relaydns/core/cryptoops"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdsec"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdverb"
	"github.com/hashicorp/yamux"
	"github.com/valyala/bytebufferpool"
)

type StreamContext struct {
	Server       *RelayServer
	Stream       *yamux.Stream
	Connection   *Connection
	ConnectionID int64
	Hijacked     *bool
}

func (ctx *StreamContext) Hijack() {
	*ctx.Hijacked = true
}

func (g *RelayServer) handleRelayInfoRequest(ctx *StreamContext, packet *rdverb.Packet) error {
	_, err := decodeProtobuf[*rdverb.RelayInfoRequest](packet.Payload)
	if err != nil {
		return err
	}

	var resp rdverb.RelayInfoResponse
	resp.RelayInfo = g.relayInfo()
	response, err := resp.MarshalVT()
	if err != nil {
		return err
	}

	return writePacket(ctx.Stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_RELAY_INFO_RESPONSE,
		Payload: response,
	})
}

func (g *RelayServer) handleLeaseUpdateRequest(ctx *StreamContext, packet *rdverb.Packet) error {
	var signedPayload rdsec.SignedPayload
	err := signedPayload.UnmarshalVT(packet.Payload)
	if err != nil {
		return err
	}

	var req rdverb.LeaseUpdateRequest
	err = req.UnmarshalVT(signedPayload.Data)
	if err != nil {
		return err
	}

	if !cryptoops.VerifySignedPayload(&signedPayload, req.Lease.Identity) {
		return err
	}

	var resp rdverb.LeaseUpdateResponse

	// Update lease in lease manager
	if g.leaseManager.UpdateLease(req.Lease, ctx.ConnectionID) {
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED

		// Register lease connection
		leaseID := string(req.Lease.Identity.Id)
		g.leaseConnectionsLock.Lock()
		g.leaseConnections[leaseID] = ctx.Connection
		g.leaseConnectionsLock.Unlock()
	} else {
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_INVALID_EXPIRES
	}

	response, err := resp.MarshalVT()
	if err != nil {
		return err
	}

	return writePacket(ctx.Stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_LEASE_UPDATE_RESPONSE,
		Payload: response,
	})
}

func (g *RelayServer) handleLeaseDeleteRequest(ctx *StreamContext, packet *rdverb.Packet) error {
	var signedPayload rdsec.SignedPayload
	err := signedPayload.UnmarshalVT(packet.Payload)
	if err != nil {
		return err
	}

	var req rdverb.LeaseDeleteRequest
	err = req.UnmarshalVT(signedPayload.Data)
	if err != nil {
		return err
	}

	if !cryptoops.VerifySignedPayload(&signedPayload, req.Identity) {
		return err
	}

	var resp rdverb.LeaseDeleteResponse

	// Delete lease from lease manager
	if g.leaseManager.DeleteLease(req.Identity) {
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED

		// Remove lease connection
		leaseID := string(req.Identity.Id)
		g.leaseConnectionsLock.Lock()
		delete(g.leaseConnections, leaseID)
		g.leaseConnectionsLock.Unlock()
	} else {
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_INVALID_IDENTITY
	}

	response, err := resp.MarshalVT()
	if err != nil {
		return err
	}

	return writePacket(ctx.Stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_LEASE_DELETE_RESPONSE,
		Payload: response,
	})
}

func (g *RelayServer) handleConnectionRequest(ctx *StreamContext, packet *rdverb.Packet) error {
	var req rdverb.ConnectionRequest
	err := req.UnmarshalVT(packet.Payload)
	if err != nil {
		return err
	}

	var resp rdverb.ConnectionResponse

	// Check if lease exists and get lease connection
	leaseEntry, exists := g.leaseManager.GetLease(req.ClientIdentity)
	if !exists {
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_INVALID_IDENTITY
	} else {
		// Get the lease connection using connection ID
		g.connectionsLock.RLock()
		leaseConn, leaseExists := g.connections[leaseEntry.ConnectionID]
		g.connectionsLock.RUnlock()

		if !leaseExists {
			resp.Code = rdverb.ResponseCode_RESPONSE_CODE_INVALID_IDENTITY
		} else {
			// Forward the connection request to the lease holder
			forwardResp, err := g.forwardConnectionRequest(leaseConn, &req)
			if err != nil {
				resp.Code = rdverb.ResponseCode_RESPONSE_CODE_REJECTED
			} else {
				resp.Code = forwardResp.Code

				// If accepted, set up bidirectional forwarding
				if resp.Code == rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
					ctx.Hijack()
					go g.setupBidirectionalForwarding(ctx.Stream, leaseConn, leaseEntry)
				}
			}
		}
	}

	response, err := resp.MarshalVT()
	if err != nil {
		return err
	}

	return writePacket(ctx.Stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
		Payload: response,
	})
}

func (g *RelayServer) forwardConnectionRequest(leaseConn *Connection, req *rdverb.ConnectionRequest) (*rdverb.ConnectionResponse, error) {
	// Open a new stream to the lease holder
	forwardStream, err := leaseConn.sess.OpenStream()
	if err != nil {
		return nil, err
	}
	defer forwardStream.Close()

	// Forward the connection request
	requestPayload, err := req.MarshalVT()
	if err != nil {
		return nil, err
	}

	err = writePacket(forwardStream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_REQUEST,
		Payload: requestPayload,
	})
	if err != nil {
		return nil, err
	}

	// Read the response
	respPacket, err := readPacket(forwardStream)
	if err != nil {
		return nil, err
	}

	if respPacket.Type != rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE {
		return nil, err
	}

	var resp rdverb.ConnectionResponse
	err = resp.UnmarshalVT(respPacket.Payload)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (g *RelayServer) setupBidirectionalForwarding(clientStream *yamux.Stream, leaseConn *Connection, leaseEntry *LeaseEntry) {
	// Open a new stream to the lease holder for data forwarding
	dataStream, err := leaseConn.sess.OpenStream()
	if err != nil {
		return
	}
	defer dataStream.Close()

	// Send a special packet to indicate this is a data stream
	initPacket := &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_REQUEST, // Reuse as init signal
		Payload: []byte("DATA_STREAM"),
	}

	err = writePacket(dataStream, initPacket)
	if err != nil {
		return
	}

	// Read the response to confirm data stream is ready
	respPacket, err := readPacket(dataStream)
	if err != nil {
		return
	}

	// Check if we got a data stream confirmation
	if respPacket.Type != rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE {
		return
	}

	var resp rdverb.ConnectionResponse
	err = resp.UnmarshalVT(respPacket.Payload)
	if err != nil {
		return
	}

	if resp.Code != rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
		return
	}

	// Track this relayed connection
	leaseID := string(leaseEntry.Lease.Identity.Id)
	g.relayedConnectionsLock.Lock()
	g.relayedConnections[leaseID] = append(g.relayedConnections[leaseID], clientStream)
	g.relayedConnectionsLock.Unlock()

	// Set up bidirectional copying with cleanup
	var wg sync.WaitGroup
	wg.Add(2)

	// Copy from client to lease holder
	go func() {
		defer wg.Done()
		io.Copy(dataStream, clientStream)
	}()

	// Copy from lease holder to client
	go func() {
		defer wg.Done()
		io.Copy(clientStream, dataStream)
	}()

	wg.Wait()

	// Clean up relayed connection tracking when done
	g.relayedConnectionsLock.Lock()
	if streams, exists := g.relayedConnections[leaseID]; exists {
		// Remove this stream from the slice
		for i, stream := range streams {
			if stream == clientStream {
				g.relayedConnections[leaseID] = append(streams[:i], streams[i+1:]...)
				break
			}
		}
		// If no more streams for this lease, remove the entry
		if len(g.relayedConnections[leaseID]) == 0 {
			delete(g.relayedConnections, leaseID)
		}
	}
	g.relayedConnectionsLock.Unlock()
}

// Helper function to read packet from stream
func readPacket(stream io.Reader) (*rdverb.Packet, error) {
	var size [4]byte

	_, err := io.ReadFull(stream, size[:])
	if err != nil {
		return nil, err
	}

	n := int(binary.BigEndian.Uint32(size[:]))
	if n > _MAX_RAW_PACKET_SIZE {
		return nil, err
	}

	buffer := bytebufferpool.Get()
	defer bytebufferpool.Put(buffer)

	bufferGrow(buffer, n)

	_, err = io.ReadFull(stream, buffer.B[:n])
	if err != nil {
		return nil, err
	}

	var packet rdverb.Packet
	err = packet.UnmarshalVT(buffer.B[:n])
	if err != nil {
		return nil, err
	}

	return &packet, nil
}
