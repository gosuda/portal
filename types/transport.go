package types

import (
	"encoding/binary"
	"errors"
)

// ErrDatagramTooSmall is returned when a datagram payload is too short to
// contain a valid flow ID varint.
var ErrDatagramTooSmall = errors.New("datagram too small to decode")

// DatagramFrame is the wire format for QUIC DATAGRAM payloads.
// Layout: [flowID varint][payload bytes]
type DatagramFrame struct {
	FlowID  uint32
	Payload []byte
}

// EncodeDatagram serialises a flow-framed datagram for transmission.
func EncodeDatagram(flowID uint32, payload []byte) []byte {
	var buf [binary.MaxVarintLen32]byte
	n := binary.PutUvarint(buf[:], uint64(flowID))
	out := make([]byte, n+len(payload))
	copy(out, buf[:n])
	copy(out[n:], payload)
	return out
}

// DecodeDatagram deserialises a flow-framed datagram.
func DecodeDatagram(data []byte) (DatagramFrame, error) {
	flowID, n := binary.Uvarint(data)
	if n <= 0 {
		return DatagramFrame{}, ErrDatagramTooSmall
	}
	return DatagramFrame{
		FlowID:  uint32(flowID),
		Payload: data[n:],
	}, nil
}
