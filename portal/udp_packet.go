package portal

import (
	"crypto/rand"
	"errors"
	"io"
)

// UDP Packet Format:
// [1 byte: Type] [16 bytes: Session Token] [N bytes: Data]
//
// Types:
// - 0x01: REGISTER - Initial session registration (sent via TCP, not UDP)
// - 0x02: DATA - Game/application data
// - 0x03: KEEPALIVE - Connection keepalive ping

const (
	UDPPacketTypeRegister   byte = 0x01
	UDPPacketTypeData       byte = 0x02
	UDPPacketTypeKeepalive  byte = 0x03

	UDPSessionTokenSize = 16
	UDPHeaderSize       = 1 + UDPSessionTokenSize // Type (1) + SessionToken (16)
	UDPMaxPacketSize    = 65507                   // Max UDP packet size (65535 - 8 UDP header - 20 IP header)
)

var (
	ErrInvalidPacketSize   = errors.New("invalid UDP packet size")
	ErrInvalidPacketType   = errors.New("invalid UDP packet type")
	ErrInvalidSessionToken = errors.New("invalid session token")
)

// UDPPacket represents a parsed UDP packet
type UDPPacket struct {
	Type         byte
	SessionToken [UDPSessionTokenSize]byte
	Data         []byte
}

// ParseUDPPacket parses a raw UDP packet into structured format
func ParseUDPPacket(raw []byte) (*UDPPacket, error) {
	if len(raw) < UDPHeaderSize {
		return nil, ErrInvalidPacketSize
	}

	packet := &UDPPacket{
		Type: raw[0],
	}

	copy(packet.SessionToken[:], raw[1:1+UDPSessionTokenSize])

	if len(raw) > UDPHeaderSize {
		packet.Data = raw[UDPHeaderSize:]
	}

	// Validate packet type
	switch packet.Type {
	case UDPPacketTypeRegister, UDPPacketTypeData, UDPPacketTypeKeepalive:
		// Valid types
	default:
		return nil, ErrInvalidPacketType
	}

	return packet, nil
}

// EncodeUDPPacket encodes a UDP packet into wire format
func EncodeUDPPacket(packetType byte, sessionToken [UDPSessionTokenSize]byte, data []byte) ([]byte, error) {
	totalSize := UDPHeaderSize + len(data)
	if totalSize > UDPMaxPacketSize {
		return nil, ErrInvalidPacketSize
	}

	packet := make([]byte, totalSize)
	packet[0] = packetType
	copy(packet[1:1+UDPSessionTokenSize], sessionToken[:])

	if len(data) > 0 {
		copy(packet[UDPHeaderSize:], data)
	}

	return packet, nil
}

// SessionTokenToString converts a session token to a hex string for logging
func SessionTokenToString(token [UDPSessionTokenSize]byte) string {
	return string(token[:])
}

// StringToSessionToken converts a string to a session token
func StringToSessionToken(s string) ([UDPSessionTokenSize]byte, error) {
	var token [UDPSessionTokenSize]byte
	if len(s) != UDPSessionTokenSize {
		return token, ErrInvalidSessionToken
	}
	copy(token[:], s)
	return token, nil
}

// GenerateSessionToken generates a random session token
func GenerateSessionToken() ([UDPSessionTokenSize]byte, error) {
	var token [UDPSessionTokenSize]byte
	_, err := io.ReadFull(rand.Reader, token[:])
	if err != nil {
		return token, err
	}
	return token, nil
}
