// Package sni provides TLS ClientHello parsing to extract SNI (Server Name Indication).
// This is used for routing TLS connections in the TLS passthrough architecture.
package sni

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

var (
	// ErrInvalidTLSRecord is returned when the TLS record is malformed
	ErrInvalidTLSRecord = errors.New("invalid TLS record")
	// ErrNotClientHello is returned when the record is not a ClientHello
	ErrNotClientHello = errors.New("not a ClientHello message")
	// ErrNoSNI is returned when the ClientHello doesn't contain SNI
	ErrNoSNI = errors.New("no SNI found in ClientHello")
	// ErrInvalidSNI is returned when the SNI hostname is invalid
	ErrInvalidSNI = errors.New("invalid SNI hostname")
)

// ExtractSNI extracts the SNI hostname from a TLS ClientHello message.
// It reads from the provided reader and returns the SNI hostname.
// The reader should be positioned at the start of the TLS record.
func ExtractSNI(r io.Reader) (string, error) {
	// Read the TLS record header (5 bytes)
	// ContentType (1) + Version (2) + Length (2)
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return "", fmt.Errorf("reading TLS header: %w", err)
	}

	// Check ContentType (0x16 = Handshake)
	if header[0] != 0x16 {
		return "", ErrNotClientHello
	}

	// Read the handshake message length
	recordLen := binary.BigEndian.Uint16(header[3:5])
	if recordLen < 4 {
		return "", ErrInvalidTLSRecord
	}

	// Read the full handshake message
	record := make([]byte, recordLen)
	if _, err := io.ReadFull(r, record); err != nil {
		return "", fmt.Errorf("reading TLS record: %w", err)
	}

	// Parse the handshake message
	return parseHandshake(record)
}

// parseHandshake parses a TLS handshake message and extracts SNI.
func parseHandshake(data []byte) (string, error) {
	if len(data) < 4 {
		return "", ErrInvalidTLSRecord
	}

	// HandshakeType (1) + Length (3)
	handshakeType := data[0]
	declaredLen := int(data[1])<<16 | int(data[2])<<8 | int(data[3])
	if declaredLen < 0 || declaredLen > len(data)-4 {
		return "", ErrInvalidTLSRecord
	}

	// Check if it's a ClientHello (0x01)
	if handshakeType != 0x01 {
		return "", ErrNotClientHello
	}

	// Skip handshake header (4 bytes)
	return parseClientHello(data[4 : 4+declaredLen])
}

// parseClientHello parses a ClientHello message and extracts SNI.
func parseClientHello(data []byte) (string, error) {
	// client_version(2) + random(32) + session_id_len(1)
	if len(data) < 35 {
		return "", ErrInvalidTLSRecord
	}

	offset := 0

	// Client Version (2 bytes)
	offset += 2

	// Random (32 bytes)
	offset += 32

	if offset >= len(data) {
		return "", ErrInvalidTLSRecord
	}

	// Session ID Length (1 byte) + Session ID
	sessionIDLen := int(data[offset])
	offset += 1 + sessionIDLen

	if offset > len(data) {
		return "", ErrInvalidTLSRecord
	}

	// Cipher Suites Length (2 bytes) + Cipher Suites
	if offset+2 > len(data) {
		return "", ErrInvalidTLSRecord
	}
	cipherSuitesLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	offset += 2 + cipherSuitesLen

	if offset > len(data) {
		return "", ErrInvalidTLSRecord
	}

	// Compression Methods Length (1 byte) + Compression Methods
	if offset+1 > len(data) {
		return "", ErrInvalidTLSRecord
	}
	compressionMethodsLen := int(data[offset])
	offset += 1 + compressionMethodsLen

	if offset > len(data) {
		return "", ErrInvalidTLSRecord
	}

	// Extensions Length (2 bytes)
	if offset+2 > len(data) {
		return "", ErrNoSNI
	}
	extensionsLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	offset += 2

	if extensionsLen == 0 || offset+extensionsLen > len(data) {
		return "", ErrNoSNI
	}

	// Parse extensions
	extensions := data[offset : offset+extensionsLen]
	return parseExtensions(extensions)
}

// parseExtensions parses TLS extensions and extracts SNI.
func parseExtensions(data []byte) (string, error) {
	offset := 0

	for offset < len(data) {
		if offset+4 > len(data) {
			return "", ErrInvalidTLSRecord
		}

		// Extension Type (2 bytes)
		extType := binary.BigEndian.Uint16(data[offset : offset+2])
		offset += 2

		// Extension Length (2 bytes)
		extLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		offset += 2

		if offset+extLen > len(data) {
			return "", ErrInvalidTLSRecord
		}

		// Extension Type 0x0000 = server_name (SNI)
		if extType == 0x0000 {
			return parseSNIExtension(data[offset : offset+extLen])
		}

		offset += extLen
	}

	return "", ErrNoSNI
}

// parseSNIExtension parses the SNI extension and returns the hostname.
func parseSNIExtension(data []byte) (string, error) {
	if len(data) < 2 {
		return "", ErrNoSNI
	}

	// SNI List Length (2 bytes)
	listLen := int(binary.BigEndian.Uint16(data[0:2]))
	if listLen == 0 || 2+listLen > len(data) {
		return "", ErrNoSNI
	}

	offset := 2
	end := 2 + listLen

	for offset < end {
		if offset+3 > end {
			return "", ErrInvalidTLSRecord
		}

		// Name Type (1 byte)
		nameType := data[offset]
		offset++

		// Name Length (2 bytes)
		nameLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		offset += 2

		if offset+nameLen > end {
			return "", ErrInvalidTLSRecord
		}

		// Name Type 0x00 = host_name
		if nameType == 0x00 {
			if nameLen == 0 {
				return "", ErrNoSNI
			}
			hostname := string(data[offset : offset+nameLen])
			if !isValidSNIHostname(hostname) {
				return "", ErrInvalidSNI
			}
			return hostname, nil
		}

		offset += nameLen
	}

	return "", ErrNoSNI
}

// isValidSNIHostname validates that a hostname is a valid DNS name per RFC 1035 and RFC 1123.
// - Total length must not exceed 253 characters
// - Labels must be 1-63 characters
// - Labels can contain a-z, A-Z, 0-9, and hyphen
// - Labels cannot start or end with hyphen
// - No null bytes or other control characters
func isValidSNIHostname(hostname string) bool {
	if len(hostname) == 0 || len(hostname) > 253 {
		return false
	}

	// Check for null bytes and other control characters
	for i := 0; i < len(hostname); i++ {
		if hostname[i] < 0x20 || hostname[i] > 0x7E {
			return false
		}
	}

	start := 0
	for i := 0; i <= len(hostname); i++ {
		if i == len(hostname) || hostname[i] == '.' {
			label := hostname[start:i]
			if len(label) == 0 || len(label) > 63 {
				return false
			}
			// Check label characters
			for j, c := range []byte(label) {
				// Allow a-z, A-Z, 0-9, and hyphen
				if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
					return false
				}
				// Label cannot start or end with hyphen
				if c == '-' && (j == 0 || j == len(label)-1) {
					return false
				}
			}
			start = i + 1
		}
	}

	return true
}

// PeekSNI peeks at the SNI from a connection without consuming the data.
// It returns the SNI and a new reader that includes the peeked data.
// This is useful for routing connections before fully reading them.
func PeekSNI(r io.Reader, bufSize int) (string, io.Reader, error) {
	if bufSize < 5 {
		return "", nil, fmt.Errorf("peek buffer too small: %d", bufSize)
	}

	// Read TLS record header first so we only read the exact record size.
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return "", nil, fmt.Errorf("peeking TLS header: %w", err)
	}
	if header[0] != 0x16 {
		reader := io.MultiReader(bytes.NewReader(header), r)
		return "", reader, ErrNotClientHello
	}

	recordLen := int(binary.BigEndian.Uint16(header[3:5]))
	if recordLen <= 0 {
		reader := io.MultiReader(bytes.NewReader(header), r)
		return "", reader, ErrInvalidTLSRecord
	}

	totalLen := 5 + recordLen
	if totalLen > bufSize {
		reader := io.MultiReader(bytes.NewReader(header), r)
		return "", reader, fmt.Errorf("TLS record too large for peek buffer: need %d bytes, have %d", totalLen, bufSize)
	}

	buf := make([]byte, totalLen)
	copy(buf, header)
	if _, err := io.ReadFull(r, buf[5:]); err != nil {
		reader := io.MultiReader(bytes.NewReader(buf[:5]), r)
		return "", reader, fmt.Errorf("peeking TLS record: %w", err)
	}

	// Create a reader that includes the peeked data.
	reader := io.MultiReader(bytes.NewReader(buf), r)

	sni, err := ExtractSNI(bytes.NewReader(buf))
	if err != nil {
		return "", reader, err
	}

	return sni, reader, nil
}
