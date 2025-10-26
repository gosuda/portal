package cryptoops

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// TestPipeConnection tests the basic pipe connection functionality
func TestPipeConnection(t *testing.T) {
	// Create a pipe for bidirectional communication
	reader, writer := io.Pipe()

	// Test writing and reading
	testData := []byte("Hello, world!")

	// Write data in a goroutine
	go func() {
		writer.Write(testData)
		writer.Close()
	}()

	// Read data
	receivedData := make([]byte, 1024)
	n, err := reader.Read(receivedData)
	if err != nil {
		t.Fatalf("Failed to read from pipe: %v", err)
	}

	received := receivedData[:n]
	if !bytes.Equal(received, testData) {
		t.Fatalf("Received %q, expected %q", received, testData)
	}
}

// TestLengthPrefixedWriteRead tests the length-prefixed write/read functions
func TestLengthPrefixedWriteRead(t *testing.T) {
	// Create a pipe for bidirectional communication
	reader, writer := io.Pipe()

	testData := []byte("Hello, world!")

	// Write length-prefixed data in a goroutine
	go func() {
		err := writeLengthPrefixed(writer, testData)
		if err != nil {
			t.Errorf("Failed to write length-prefixed data: %v", err)
		}
		writer.Close()
	}()

	// Read length-prefixed data
	receivedData, err := readLengthPrefixed(reader)
	if err != nil {
		t.Fatalf("Failed to read length-prefixed data: %v", err)
	}

	if !bytes.Equal(receivedData, testData) {
		t.Fatalf("Received %q, expected %q", receivedData, testData)
	}
}

// TestPipeConn tests the pipe connection wrapper
func TestPipeConn(t *testing.T) {
	// Create a pipe for bidirectional communication
	reader, writer := io.Pipe()

	// Create pipe connection
	conn := &pipeConn{
		reader: reader,
		writer: writer,
	}

	testData := []byte("Hello, world!")

	// Write data in a goroutine
	go func() {
		n, err := conn.Write(testData)
		if err != nil {
			t.Errorf("Failed to write to pipe connection: %v", err)
		}
		if n != len(testData) {
			t.Errorf("Wrote %d bytes, expected %d", n, len(testData))
		}
		conn.Close()
	}()

	// Read data
	receivedData := make([]byte, 1024)
	n, err := conn.Read(receivedData)
	if err != nil {
		t.Fatalf("Failed to read from pipe connection: %v", err)
	}

	received := receivedData[:n]
	if !bytes.Equal(received, testData) {
		t.Fatalf("Received %q, expected %q", received, testData)
	}
}

// TestBidirectionalPipeConn tests bidirectional communication with pipe connections
func TestBidirectionalPipeConn(t *testing.T) {
	// Create pipes for bidirectional communication
	clientReader, clientWriter := io.Pipe()
	serverReader, serverWriter := io.Pipe()

	// Create pipe connections
	clientConn := &pipeConn{
		reader: clientReader,
		writer: serverWriter,
	}
	serverConn := &pipeConn{
		reader: serverReader,
		writer: clientWriter,
	}

	clientData := []byte("Hello from client!")
	serverData := []byte("Hello from server!")

	// Use channels to coordinate
	done := make(chan bool, 2)

	// Client writes data and reads response
	go func() {
		defer func() { done <- true }()

		// Client writes data
		n, err := clientConn.Write(clientData)
		if err != nil {
			t.Errorf("Failed to write from client: %v", err)
			return
		}
		if n != len(clientData) {
			t.Errorf("Client wrote %d bytes, expected %d", n, len(clientData))
			return
		}

		// Client reads response
		clientReadBuf := make([]byte, 1024)
		n, err = clientConn.Read(clientReadBuf)
		if err != nil {
			t.Errorf("Failed to read at client: %v", err)
			return
		}

		receivedServerData := clientReadBuf[:n]
		if !bytes.Equal(receivedServerData, serverData) {
			t.Errorf("Client received %q, expected %q", receivedServerData, serverData)
		}
	}()

	// Server reads data and writes response
	go func() {
		defer func() { done <- true }()

		// Server reads data
		serverReadBuf := make([]byte, 1024)
		n, err := serverConn.Read(serverReadBuf)
		if err != nil {
			t.Errorf("Failed to read at server: %v", err)
			return
		}

		receivedClientData := serverReadBuf[:n]
		if !bytes.Equal(receivedClientData, clientData) {
			t.Errorf("Server received %q, expected %q", receivedClientData, clientData)
			return
		}

		// Server writes response
		n, err = serverConn.Write(serverData)
		if err != nil {
			t.Errorf("Failed to write from server: %v", err)
			return
		}
		if n != len(serverData) {
			t.Errorf("Server wrote %d bytes, expected %d", n, len(serverData))
		}
	}()

	// Wait for both goroutines to complete
	for i := 0; i < 2; i++ {
		select {
		case <-done:
			// One goroutine completed
		case <-time.After(5 * time.Second):
			t.Fatal("Test timed out")
		}
	}

	// Close connections
	clientConn.Close()
	serverConn.Close()
}
