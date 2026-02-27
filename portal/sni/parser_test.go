package sni

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

func TestExtractSNI(t *testing.T) {
	clientHello := buildClientHello("example.com", true)

	sni, err := ExtractSNI(bytes.NewReader(clientHello))
	if err != nil {
		t.Fatalf("ExtractSNI failed: %v", err)
	}
	if sni != "example.com" {
		t.Errorf("Expected SNI 'example.com', got '%s'", sni)
	}
}

func TestExtractSNI_NoSNI(t *testing.T) {
	clientHello := buildClientHello("", false)

	_, err := ExtractSNI(bytes.NewReader(clientHello))
	if err != ErrNoSNI {
		t.Errorf("Expected ErrNoSNI, got: %v", err)
	}
}

func TestExtractSNI_NotClientHello(t *testing.T) {
	serverHello := buildTLSRecord(0x02, nil)

	_, err := ExtractSNI(bytes.NewReader(serverHello))
	if err != ErrNotClientHello {
		t.Errorf("Expected ErrNotClientHello, got: %v", err)
	}
}

func TestPeekSNI(t *testing.T) {
	clientHello := buildClientHello("example.com", true)

	sni, reader, err := PeekSNI(bytes.NewReader(clientHello), 4096)
	if err != nil {
		t.Fatalf("PeekSNI failed: %v", err)
	}
	if sni != "example.com" {
		t.Errorf("Expected SNI 'example.com', got '%s'", sni)
	}

	// Verify we can still read the full ClientHello from the returned reader.
	buf, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("Reading from returned reader failed: %v", err)
	}
	if !bytes.Equal(buf, clientHello) {
		t.Error("Returned reader doesn't contain the full ClientHello")
	}
}

func TestExtractSNI_TruncatedClientHello(t *testing.T) {
	record := buildTLSRecord(0x01, make([]byte, 34)) // 1 byte short for session_id_len field access
	_, err := ExtractSNI(bytes.NewReader(record))
	if err != ErrInvalidTLSRecord {
		t.Fatalf("expected ErrInvalidTLSRecord, got: %v", err)
	}
}

func TestExtractSNI_InvalidHandshakeLength(t *testing.T) {
	record := buildTLSRecord(0x01, []byte{0x03, 0x03, 0x00, 0x00, 0x00})
	// Corrupt handshake declared length to exceed available bytes
	record[6] = 0x00
	record[7] = 0x01
	record[8] = 0x00

	_, err := ExtractSNI(bytes.NewReader(record))
	if err != ErrInvalidTLSRecord {
		t.Fatalf("expected ErrInvalidTLSRecord, got: %v", err)
	}
}

func TestPeekSNI_NotClientHelloPreservesData(t *testing.T) {
	payload := []byte("plaintext")
	buf := append([]byte{0x17, 0x03, 0x03, 0x00, byte(len(payload))}, payload...)

	_, reader, err := PeekSNI(bytes.NewReader(buf), 4096)
	if err != ErrNotClientHello {
		t.Fatalf("expected ErrNotClientHello, got: %v", err)
	}

	got, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatalf("failed to read returned reader: %v", readErr)
	}
	if !bytes.Equal(got, buf) {
		t.Fatalf("returned reader did not preserve original bytes")
	}
}

func TestPeekSNI_RecordTooLarge(t *testing.T) {
	clientHello := buildClientHello(strings.Repeat("a", 10)+".example.com", true)

	_, reader, err := PeekSNI(bytes.NewReader(clientHello), 64)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected record too large error, got: %v", err)
	}

	got, readErr := io.ReadAll(reader)
	if readErr != nil {
		t.Fatalf("failed to read returned reader: %v", readErr)
	}
	if !bytes.Equal(got, clientHello) {
		t.Fatalf("returned reader did not preserve original bytes")
	}
}

func buildClientHello(sni string, includeSNI bool) []byte {
	body := make([]byte, 0, 128)
	body = append(body, 0x03, 0x03) // TLS 1.2
	body = append(body, make([]byte, 32)...)
	body = append(body, 0x00)         // Session ID length
	body = append(body, 0x00, 0x02)   // Cipher Suites length
	body = append(body, 0x00, 0x2f)   // TLS_RSA_WITH_AES_128_CBC_SHA
	body = append(body, 0x01, 0x00)   // Compression methods
	extensions := make([]byte, 0, 64) // Extensions
	if includeSNI {
		host := []byte(sni)
		sniData := make([]byte, 2+1+2+len(host)) // list_len + name_type + name_len + host
		binary.BigEndian.PutUint16(sniData[0:2], uint16(1+2+len(host)))
		sniData[2] = 0x00 // host_name
		binary.BigEndian.PutUint16(sniData[3:5], uint16(len(host)))
		copy(sniData[5:], host)

		ext := make([]byte, 4+len(sniData)) // ext_type + ext_len + ext_data
		binary.BigEndian.PutUint16(ext[0:2], 0x0000)
		binary.BigEndian.PutUint16(ext[2:4], uint16(len(sniData)))
		copy(ext[4:], sniData)
		extensions = append(extensions, ext...)
	}
	body = append(body, byte(len(extensions)>>8), byte(len(extensions)))
	body = append(body, extensions...)

	return buildTLSRecord(0x01, body)
}

func buildTLSRecord(handshakeType byte, handshakeBody []byte) []byte {
	handshake := make([]byte, 4+len(handshakeBody))
	handshake[0] = handshakeType
	handshakeLen := len(handshakeBody)
	handshake[1] = byte(handshakeLen >> 16)
	handshake[2] = byte(handshakeLen >> 8)
	handshake[3] = byte(handshakeLen)
	copy(handshake[4:], handshakeBody)

	record := make([]byte, 5+len(handshake))
	record[0] = 0x16 // Handshake
	record[1] = 0x03 // TLS 1.x
	record[2] = 0x01 // TLS 1.0 record version
	recordLen := len(handshake)
	binary.BigEndian.PutUint16(record[3:5], uint16(recordLen))
	copy(record[5:], handshake)
	return record
}
