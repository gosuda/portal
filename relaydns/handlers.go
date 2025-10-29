package relaydns

import (
	"encoding/binary"
	"io"
	"sync"

	"github.com/gosuda/relaydns/relaydns/core/cryptoops"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdsec"
	"github.com/gosuda/relaydns/relaydns/core/proto/rdverb"
	"github.com/hashicorp/yamux"
	"github.com/rs/zerolog/log"
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

		// Log lease update completion
		log.Debug().
			Str("lease_id", leaseID).
			Str("lease_name", req.Lease.Name).
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

	var resp rdverb.ConnectionResponse

	// Check if lease exists and get lease connection using LeaseId
	leaseEntry, exists := g.leaseManager.GetLeaseByID(req.LeaseId)
	if !exists {
		log.Warn().Str("lease_id", req.LeaseId).Msg("[RelayServer] Lease not found")
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_INVALID_IDENTITY

		response, err := resp.MarshalVT()
		if err != nil {
			log.Error().Err(err).Msg("[RelayServer] Failed to marshal connection response")
			return err
		}

		return writePacket(ctx.Stream, &rdverb.Packet{
			Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
			Payload: response,
		})
	}

	log.Debug().
		Str("lease_id", req.LeaseId).
		Int64("lease_conn_id", leaseEntry.ConnectionID).
		Msg("[RelayServer] Lease found, forwarding to lease holder")

	// Get the lease connection using connection ID
	g.connectionsLock.RLock()
	leaseConn, leaseExists := g.connections[leaseEntry.ConnectionID]
	g.connectionsLock.RUnlock()

	if !leaseExists {
		log.Warn().
			Str("lease_id", req.LeaseId).
			Int64("lease_conn_id", leaseEntry.ConnectionID).
			Msg("[RelayServer] Lease connection no longer active")
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_INVALID_IDENTITY

		response, err := resp.MarshalVT()
		if err != nil {
			log.Error().Err(err).Msg("[RelayServer] Failed to marshal connection response")
			return err
		}

		return writePacket(ctx.Stream, &rdverb.Packet{
			Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
			Payload: response,
		})
	}

	// Open a stream to the lease holder
	log.Debug().Str("lease_id", req.LeaseId).Msg("[RelayServer] Opening stream to lease holder")
	leaseStream, err := leaseConn.sess.OpenStream()
	if err != nil {
		log.Error().Err(err).Str("lease_id", req.LeaseId).Msg("[RelayServer] Failed to open stream to lease holder")
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_REJECTED

		response, err := resp.MarshalVT()
		if err != nil {
			return err
		}

		return writePacket(ctx.Stream, &rdverb.Packet{
			Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
			Payload: response,
		})
	}

	// Forward the connection request
	requestPayload, err := req.MarshalVT()
	if err != nil {
		log.Error().Err(err).Msg("[RelayServer] Failed to marshal forward request")
		leaseStream.Close()
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_REJECTED

		response, err := resp.MarshalVT()
		if err != nil {
			return err
		}

		return writePacket(ctx.Stream, &rdverb.Packet{
			Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
			Payload: response,
		})
	}

	log.Debug().Str("lease_id", req.LeaseId).Msg("[RelayServer] Sending connection request to lease holder")
	err = writePacket(leaseStream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_REQUEST,
		Payload: requestPayload,
	})
	if err != nil {
		log.Error().Err(err).Msg("[RelayServer] Failed to write forward request")
		leaseStream.Close()
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_REJECTED

		response, err := resp.MarshalVT()
		if err != nil {
			return err
		}

		return writePacket(ctx.Stream, &rdverb.Packet{
			Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
			Payload: response,
		})
	}

	// Read the response
	log.Debug().Str("lease_id", req.LeaseId).Msg("[RelayServer] Waiting for response from lease holder")
	respPacket, err := readPacket(leaseStream)
	if err != nil {
		log.Error().Str("lease_id", req.LeaseId).Err(err).Msg("[RelayServer] Failed to read forward response")
		leaseStream.Close()
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_REJECTED

		response, err := resp.MarshalVT()
		if err != nil {
			return err
		}

		return writePacket(ctx.Stream, &rdverb.Packet{
			Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
			Payload: response,
		})
	}

	if respPacket.Type != rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE {
		log.Warn().Str("packet_type", respPacket.Type.String()).Msg("[RelayServer] Unexpected response packet type")
		leaseStream.Close()
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_REJECTED

		response, err := resp.MarshalVT()
		if err != nil {
			return err
		}

		return writePacket(ctx.Stream, &rdverb.Packet{
			Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
			Payload: response,
		})
	}

	err = resp.UnmarshalVT(respPacket.Payload)
	if err != nil {
		log.Error().Err(err).Msg("[RelayServer] Failed to unmarshal forward response")
		leaseStream.Close()
		resp.Code = rdverb.ResponseCode_RESPONSE_CODE_REJECTED

		response, err := resp.MarshalVT()
		if err != nil {
			return err
		}

		return writePacket(ctx.Stream, &rdverb.Packet{
			Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
			Payload: response,
		})
	}

	log.Debug().
		Str("lease_id", req.LeaseId).
		Str("response_code", resp.Code.String()).
		Msg("[RelayServer] Received response from lease holder, sending to client")

	// Send response to client
	response, err := resp.MarshalVT()
	if err != nil {
		log.Error().Err(err).Msg("[RelayServer] Failed to marshal connection response")
		leaseStream.Close()
		return err
	}

	err = writePacket(ctx.Stream, &rdverb.Packet{
		Type:    rdverb.PacketType_PACKET_TYPE_CONNECTION_RESPONSE,
		Payload: response,
	})
	if err != nil {
		log.Error().Err(err).Msg("[RelayServer] Failed to write connection response")
		leaseStream.Close()
		return err
	}

	// If accepted, hijack both streams and set up bidirectional forwarding
	if resp.Code == rdverb.ResponseCode_RESPONSE_CODE_ACCEPTED {
		log.Debug().Str("lease_id", req.LeaseId).Msg("[RelayServer] Connection accepted, setting up bidirectional forwarding")
		ctx.Hijack()

		leaseID := string(leaseEntry.Lease.Identity.Id)
		g.relayedConnectionsLock.Lock()
		g.relayedConnections[leaseID] = append(g.relayedConnections[leaseID], ctx.Stream)
		g.relayedConnectionsLock.Unlock()

		// Set up bidirectional copying
		var wg sync.WaitGroup
		wg.Add(2)

		// Copy from client to lease holder
		go func() {
			defer wg.Done()
			n, err := io.Copy(leaseStream, ctx.Stream)
			log.Debug().
				Str("lease_id", leaseID).
				Int64("bytes", n).
				Err(err).
				Msg("[RelayServer] Client -> Lease copy finished")
			leaseStream.Close()
		}()

		// Copy from lease holder to client
		go func() {
			defer wg.Done()
			n, err := io.Copy(ctx.Stream, leaseStream)
			log.Debug().
				Str("lease_id", leaseID).
				Int64("bytes", n).
				Err(err).
				Msg("[RelayServer] Lease -> Client copy finished")
			ctx.Stream.Close()
		}()

		wg.Wait()
		log.Debug().Str("lease_id", leaseID).Msg("[RelayServer] Connection forwarding completed successfully")

		// Clean up relayed connection tracking
		g.relayedConnectionsLock.Lock()
		if streams, exists := g.relayedConnections[leaseID]; exists {
			for i, stream := range streams {
				if stream == ctx.Stream {
					g.relayedConnections[leaseID] = append(streams[:i], streams[i+1:]...)
					break
				}
			}
			if len(g.relayedConnections[leaseID]) == 0 {
				delete(g.relayedConnections, leaseID)
			}
		}
		g.relayedConnectionsLock.Unlock()
	} else {
		// Connection rejected, close lease stream
		leaseStream.Close()
	}

	return nil
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
