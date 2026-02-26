// Package sni provides TLS ClientHello SNI extraction and TCP routing utilities
// for the Portal funnel feature (TLS passthrough).
package sni

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"slices"
)

// maxTLSRecordSize is the maximum size of a TLS record (5-byte header + 16KB payload + overhead).
const maxTLSRecordSize = 18432 // 18KB

// Sentinel errors for SNI parsing.
var (
	ErrInvalidTLSRecord = errors.New("invalid TLS record")
	ErrNotClientHello   = errors.New("not a TLS ClientHello")
	ErrNoSNI            = errors.New("no SNI extension found")
)

// TLS constants.
const (
	tlsRecordHeaderLen  = 5
	tlsContentHandshake = 0x16
	tlsHandshakeHello   = 0x01
	tlsExtensionSNI     = 0x0000
	sniHostNameType     = 0x00
)

// PeekSNI reads the TLS ClientHello from conn and extracts the SNI hostname
// without consuming the bytes. The returned io.Reader prepends the peeked bytes
// so downstream can read the full TLS handshake.
//
// Callers should set a read deadline before calling PeekSNI and clear it after:
//
//	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
//	sni, reader, err := sni.PeekSNI(conn)
//	conn.SetReadDeadline(time.Time{})
func PeekSNI(conn net.Conn) (string, io.Reader, error) {
	// Read TLS record header (5 bytes): content_type(1) + version(2) + length(2)
	header := make([]byte, tlsRecordHeaderLen)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", prependReader(header, conn), err
	}

	// Validate content type
	if header[0] != tlsContentHandshake {
		reader := prependReader(header, conn)
		return "", reader, ErrInvalidTLSRecord
	}

	// Parse record length
	recordLen := int(binary.BigEndian.Uint16(header[3:5]))
	if recordLen <= 0 || recordLen > maxTLSRecordSize-tlsRecordHeaderLen {
		reader := prependReader(header, conn)
		return "", reader, ErrInvalidTLSRecord
	}

	// Read record body
	body := make([]byte, recordLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		buf := slices.Concat(header, body)
		return "", prependReader(buf, conn), err
	}

	// Combine header + body for the prepended reader
	peeked := slices.Concat(header, body)

	// Parse ClientHello from body
	sni, err := parseClientHello(body)
	if err != nil {
		return "", prependReader(peeked, conn), err
	}

	return sni, prependReader(peeked, conn), nil
}

// parseClientHello extracts the SNI hostname from a TLS ClientHello handshake body.
func parseClientHello(data []byte) (string, error) {
	// Handshake header: type(1) + length(3)
	if len(data) < 4 {
		return "", ErrNotClientHello
	}

	if data[0] != tlsHandshakeHello {
		return "", ErrNotClientHello
	}

	// Skip handshake header (type + 3-byte length)
	offset := 4

	// Client version (2 bytes)
	if offset+2 > len(data) {
		return "", ErrNotClientHello
	}
	offset += 2

	// Random (32 bytes)
	if offset+32 > len(data) {
		return "", ErrNotClientHello
	}
	offset += 32

	// Session ID (variable length)
	if offset+1 > len(data) {
		return "", ErrNotClientHello
	}
	sessionIDLen := int(data[offset])
	offset++
	if offset+sessionIDLen > len(data) {
		return "", ErrNotClientHello
	}
	offset += sessionIDLen

	// Cipher suites (2-byte length + data)
	if offset+2 > len(data) {
		return "", ErrNotClientHello
	}
	cipherSuitesLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	offset += 2
	if offset+cipherSuitesLen > len(data) {
		return "", ErrNotClientHello
	}
	offset += cipherSuitesLen

	// Compression methods (1-byte length + data)
	if offset+1 > len(data) {
		return "", ErrNotClientHello
	}
	compressionLen := int(data[offset])
	offset++
	if offset+compressionLen > len(data) {
		return "", ErrNotClientHello
	}
	offset += compressionLen

	// Extensions (2-byte total length + extension data)
	if offset+2 > len(data) {
		return "", ErrNoSNI
	}
	extensionsLen := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	offset += 2

	if offset+extensionsLen > len(data) {
		return "", ErrNoSNI
	}

	return parseSNIExtension(data[offset : offset+extensionsLen])
}

// parseSNIExtension scans TLS extensions for the SNI extension and extracts the hostname.
func parseSNIExtension(extensions []byte) (string, error) {
	offset := 0

	for offset+4 <= len(extensions) {
		extType := binary.BigEndian.Uint16(extensions[offset : offset+2])
		extLen := int(binary.BigEndian.Uint16(extensions[offset+2 : offset+4]))
		offset += 4

		if offset+extLen > len(extensions) {
			break
		}

		if extType == tlsExtensionSNI {
			return parseSNIPayload(extensions[offset : offset+extLen])
		}

		offset += extLen
	}

	return "", ErrNoSNI
}

// parseSNIPayload parses the SNI extension payload to extract the hostname.
func parseSNIPayload(data []byte) (string, error) {
	// SNI list length (2 bytes)
	if len(data) < 2 {
		return "", ErrNoSNI
	}
	listLen := int(binary.BigEndian.Uint16(data[0:2]))
	if 2+listLen > len(data) {
		return "", ErrNoSNI
	}

	offset := 2
	end := 2 + listLen

	for offset+3 <= end {
		nameType := data[offset]
		nameLen := int(binary.BigEndian.Uint16(data[offset+1 : offset+3]))
		offset += 3

		if offset+nameLen > end {
			return "", ErrNoSNI
		}

		if nameType == sniHostNameType {
			return string(data[offset : offset+nameLen]), nil
		}

		offset += nameLen
	}

	return "", ErrNoSNI
}

// prependReader returns an io.Reader that first reads from the peeked bytes,
// then continues reading from the underlying connection.
func prependReader(peeked []byte, conn net.Conn) io.Reader {
	if len(peeked) == 0 {
		return conn
	}
	return io.MultiReader(bytes.NewReader(peeked), conn)
}
