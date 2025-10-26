package cryptoops

import (
	"bytes"
	"io"
	"testing"
)

// TestPacketSizeLimit tests that packets larger than the limit are rejected
func TestPacketSizeLimit(t *testing.T) {
	// Create a mock connection that sends a packet larger than maxRawPacketSize
	largePacketSize := maxRawPacketSize + 1

	// Create a buffer with length prefix indicating a large packet
	buf := make([]byte, 4+largePacketSize)

	// Write big-endian length
	buf[0] = byte(largePacketSize >> 24)
	buf[1] = byte(largePacketSize >> 16)
	buf[2] = byte(largePacketSize >> 8)
	buf[3] = byte(largePacketSize)

	// Fill the rest with dummy data
	for i := 4; i < len(buf); i++ {
		buf[i] = 0x42
	}

	// Create a reader that returns our large packet
	reader := bytes.NewReader(buf)

	// Try to read the length-prefixed message
	_, err := readLengthPrefixed(reader)

	// Should fail with ErrHandshakeFailed
	if err != ErrHandshakeFailed {
		t.Errorf("Expected ErrHandshakeFailed for oversized packet, got: %v", err)
	}
}

// TestSecureConnectionPacketSizeLimit tests that SecureConnection rejects oversized packets
func TestSecureConnectionPacketSizeLimit(t *testing.T) {
	// Create a mock connection that sends a packet larger than maxRawPacketSize
	largePacketSize := maxRawPacketSize + 1

	// Create a buffer with length prefix indicating a large packet
	buf := make([]byte, 4+largePacketSize)

	// Write big-endian length
	buf[0] = byte(largePacketSize >> 24)
	buf[1] = byte(largePacketSize >> 16)
	buf[2] = byte(largePacketSize >> 8)
	buf[3] = byte(largePacketSize)

	// Fill the rest with dummy data
	for i := 4; i < len(buf); i++ {
		buf[i] = 0x42
	}

	// Create a pipe connection
	reader, writer := io.Pipe()

	// Write the large packet to the writer in a goroutine
	go func() {
		writer.Write(buf)
		writer.Close()
	}()

	// Create a mock secure connection using pipeConn
	sc := &SecureConnection{
		conn: &pipeConn{
			reader: reader,
			writer: writer,
		},
	}

	// Try to read from the secure connection
	readBuf := make([]byte, 1024)
	_, err := sc.Read(readBuf)

	// Should fail with ErrDecryptionFailed
	if err != ErrDecryptionFailed {
		t.Errorf("Expected ErrDecryptionFailed for oversized packet in SecureConnection, got: %v", err)
	}
}
