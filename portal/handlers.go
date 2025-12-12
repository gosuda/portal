package portal

import (
	"encoding/binary"
	"io"

	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"
	"github.com/valyala/bytebufferpool"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
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

		// Log lease update completion
		log.Debug().
			Str("lease_id", leaseID).
			Str("lease_name", req.Lease.Name).
			RawJSON("metadata", []byte(req.Lease.Metadata)).
			Int64("connection_id", ctx.ConnectionID).
			Msg("[RelayServer] Lease update completed successfully")
	} else {
		// Lease update failed (could be expired or name conflict)
		leaseID := string(req.Lease.Identity.Id)
		log.Warn().
			Str("lease_id", leaseID).
			Str("lease_name", req.Lease.Name).
			Msg("[RelayServer] Lease update rejected (expired or name conflict)")
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_REJECTED
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

		// Log lease deletion completion
		log.Debug().
			Str("lease_id", leaseID).
			Msg("[RelayServer] Lease deletion completed successfully")
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
		log.Error().Err(err).Msg("[RelayServer] Failed to unmarshal connection request")
		return err
	}

	log.Debug().
		Str("lease_id", req.LeaseId).
		Str("client_id", req.ClientIdentity.Id).
		Int64("conn_id", ctx.ConnectionID).
		Msg("[RelayServer] Handling connection request")

	// Check if lease exists and get lease connection
	leaseEntry, exists := g.leaseManager.GetLeaseByID(req.LeaseId)
	if !exists {
		return g.sendConnectionResponse(ctx.Stream, rdverb.ResponseCode_RESPONSE_CODE_INVALID_IDENTITY)
	}

	// Get the lease connection
	g.connectionsLock.RLock()
	leaseConn, leaseExists := g.connections[leaseEntry.ConnectionID]
	g.connectionsLock.RUnlock()

	if !leaseExists {
		return g.sendConnectionResponse(ctx.Stream, rdverb.ResponseCode_RESPONSE_CODE_INVALID_IDENTITY)
	}

	// Forward request to lease holder
	leaseStream, respCode, err := g.forwardConnectionRequest(leaseConn, &req)
	if err != nil {
		// If forwarding failed, we might need to close the stream if it was opened
		if leaseStream != nil {
			leaseStream.Close()
		}
		return g.sendConnectionResponse(ctx.Stream, respCode)
	}

	// Enforce relayed connection limits
	if respCode == rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
		leaseID := string(leaseEntry.Lease.Identity.Id)
		g.limitsLock.Lock()
		overPerLease := g.maxRelayedPerLease > 0 && g.relayedPerLeaseCount[leaseID] >= g.maxRelayedPerLease
		g.limitsLock.Unlock()

		if overPerLease {
			log.Warn().Str("lease_id", leaseID).Msg("[RelayServer] Relayed connection per-lease limit reached")
			respCode = rdverb.ResponseCode_RESPONSE_CODE_REJECTED
			leaseStream.Close()
		}
	}

	// Send response to client
	if err := g.sendConnectionResponse(ctx.Stream, respCode); err != nil {
		leaseStream.Close()
		return err
	}

	// If accepted, set up bidirectional forwarding
	if respCode == rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
		ctx.Hijack()
		go g.establishRelayedConnection(ctx.Stream, leaseStream, string(leaseEntry.Lease.Identity.Id))
	} else {
		leaseStream.Close()
	}

	return nil
}

// forwardConnectionRequest opens a stream to the lease holder and forwards the request
func (g *RelayServer) forwardConnectionRequest(leaseConn *Connection, req *rdverb.ConnectionRequest) (*yamux.Stream, rdverb.ResponseCode, error) {
	leaseStream, err := leaseConn.sess.OpenStream()
	if err != nil {
		return nil, rdverb.ResponseCode_RESPONSE_CODE_REJECTED, err
	}

	reqPayload, err := req.MarshalVT()
	if err != nil {
		leaseStream.Close()
		return nil, rdverb.ResponseCode_RESPONSE_CODE_REJECTED, err
	}

	err = writePacket(leaseStream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_REQUEST,
		Payload: reqPayload,
	})
	if err != nil {
		leaseStream.Close()
		return nil, rdverb.ResponseCode_RESPONSE_CODE_REJECTED, err
	}

	respPacket, err := readPacket(leaseStream)
	if err != nil {
		leaseStream.Close()
		return nil, rdverb.ResponseCode_RESPONSE_CODE_REJECTED, err
	}

	if respPacket.Type != rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE {
		leaseStream.Close()
		return nil, rdverb.ResponseCode_RESPONSE_CODE_REJECTED, nil
	}

	var resp rdverb.ConnectionResponse
	if err := resp.UnmarshalVT(respPacket.Payload); err != nil {
		leaseStream.Close()
		return nil, rdverb.ResponseCode_RESPONSE_CODE_REJECTED, err
	}

	return leaseStream, resp.Code, nil
}

func (g *RelayServer) sendConnectionResponse(stream *yamux.Stream, code rdverb.ResponseCode) error {
	resp := rdverb.ConnectionResponse{Code: code}
	payload, err := resp.MarshalVT()
	if err != nil {
		return err
	}
	return writePacket(stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
		Payload: payload,
	})
}

func (g *RelayServer) establishRelayedConnection(clientStream, leaseStream *yamux.Stream, leaseID string) {
	// Register connection for tracking
	g.limitsLock.Lock()
	g.relayedPerLeaseCount[leaseID]++
	g.limitsLock.Unlock()

	g.relayedConnectionsLock.Lock()
	g.relayedConnections[leaseID] = append(g.relayedConnections[leaseID], clientStream)
	g.relayedConnectionsLock.Unlock()

	// Cleanup function
	defer func() {
		g.limitsLock.Lock()
		if g.relayedPerLeaseCount[leaseID] > 0 {
			g.relayedPerLeaseCount[leaseID]--
		}
		g.limitsLock.Unlock()

		g.relayedConnectionsLock.Lock()
		if streams, ok := g.relayedConnections[leaseID]; ok {
			for i, s := range streams {
				if s == clientStream {
					g.relayedConnections[leaseID] = append(streams[:i], streams[i+1:]...)
					break
				}
			}
		}
		g.relayedConnectionsLock.Unlock()
	}()

	// Use callback for actual relay (handles BPS limiting in relay-server)
	if g.onEstablishRelay != nil {
		g.onEstablishRelay(clientStream, leaseStream, leaseID)
	} else {
		// Fallback: simple copy without rate limiting
		go func() {
			io.Copy(leaseStream, clientStream)
			leaseStream.Close()
		}()
		io.Copy(clientStream, leaseStream)
		clientStream.Close()
	}
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
