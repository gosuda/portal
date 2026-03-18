package types

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// LeaseCapabilities describes which data planes a lease exposes.
// Stream maps to reverse TCP/TLS sessions; Datagram maps to QUIC/UDP.
type LeaseCapabilities struct {
	Datagram bool
	Stream   bool
}

// ParseLeaseCapabilities normalizes the public transport string into the
// internal capability model shared by relay and SDK.
func ParseLeaseCapabilities(raw string) (LeaseCapabilities, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", TransportTCP:
		return LeaseCapabilities{Stream: true}, nil
	case TransportUDP:
		return LeaseCapabilities{Datagram: true}, nil
	case TransportBoth:
		return LeaseCapabilities{Datagram: true, Stream: true}, nil
	default:
		return LeaseCapabilities{}, fmt.Errorf("unsupported transport %q", strings.TrimSpace(raw))
	}
}

func (c LeaseCapabilities) SupportsDatagram() bool {
	return c.Datagram
}

func (c LeaseCapabilities) SupportsStream() bool {
	return c.Stream
}

// Transport returns the canonical public transport label for the capability set.
func (c LeaseCapabilities) Transport() string {
	switch {
	case c.Stream && c.Datagram:
		return TransportBoth
	case c.Datagram:
		return TransportUDP
	case c.Stream:
		return TransportTCP
	default:
		return ""
	}
}

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
