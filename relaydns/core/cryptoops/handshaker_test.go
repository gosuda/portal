package cryptoops

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"testing"
	"time"

	"github.com/gosuda/relaydns/relaydns/core/proto/rdsec"
)

// pipeConn implements a connection using io.Pipe for bidirectional communication
type pipeConn struct {
	reader io.Reader
	writer io.Writer
	closed bool
}

func (c *pipeConn) Read(p []byte) (n int, err error) {
	if c.closed {
		return 0, io.EOF
	}
	return c.reader.Read(p)
}

func (c *pipeConn) Write(p []byte) (n int, err error) {
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	return c.writer.Write(p)
}

func (c *pipeConn) Close() error {
	c.closed = true
	// Try to close both reader and writer if they support it
	if closer, ok := c.reader.(io.Closer); ok {
		closer.Close()
	}
	if closer, ok := c.writer.(io.Closer); ok {
		closer.Close()
	}
	return nil
}

// TestHandshake tests the full handshake process between client and server
func TestHandshake(t *testing.T) {
	// Create credentials for client and server
	clientCred, err := NewCredential()
	if err != nil {
		t.Fatalf("Failed to create client credential: %v", err)
	}

	serverCred, err := NewCredential()
	if err != nil {
		t.Fatalf("Failed to create server credential: %v", err)
	}

	// Create handshakers
	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	// Create a pipe for bidirectional communication
	clientReader, clientWriter := io.Pipe()
	serverReader, serverWriter := io.Pipe()

	// Create pipe connections
	clientPipeConn := &pipeConn{
		reader: clientReader,
		writer: serverWriter,
	}
	serverPipeConn := &pipeConn{
		reader: serverReader,
		writer: clientWriter,
	}

	alpn := "test-alpn"

	// Use channels to coordinate
	done := make(chan bool, 2)
	var clientSecureConn, serverSecureConn *SecureConnection
	var clientErr, serverErr error

	// Start server handshake in a goroutine
	go func() {
		defer func() { done <- true }()
		serverSecureConn, serverErr = serverHandshaker.ServerHandshake(serverPipeConn, alpn)
	}()

	// Give the server a moment to start waiting
	time.Sleep(10 * time.Millisecond)

	// Start client handshake in a goroutine
	go func() {
		defer func() { done <- true }()
		clientSecureConn, clientErr = clientHandshaker.ClientHandshake(clientPipeConn, alpn)
	}()

	// Wait for both handshakes to complete
	for i := 0; i < 2; i++ {
		select {
		case <-done:
			// One handshake completed
		case <-time.After(5 * time.Second):
			t.Fatal("Handshake timed out")
		}
	}

	// Check for errors
	if clientErr != nil {
		t.Fatalf("Client handshake failed: %v", clientErr)
	}
	if serverErr != nil {
		t.Fatalf("Server handshake failed: %v", serverErr)
	}

	// Test encrypted communication
	testMessage := []byte("Hello, secure world!")

	// Use channels to coordinate communication
	commDone := make(chan bool, 2)

	// Client writes encrypted message
	go func() {
		defer func() { commDone <- true }()

		n, err := clientSecureConn.Write(testMessage)
		if err != nil {
			t.Errorf("Client write failed: %v", err)
			return
		}
		if n != len(testMessage) {
			t.Errorf("Client wrote %d bytes, expected %d", n, len(testMessage))
		}

		// Client reads response
		clientReadBuf := make([]byte, 1024)
		n, err = clientSecureConn.Read(clientReadBuf)
		if err != nil {
			t.Errorf("Client read failed: %v", err)
			return
		}

		responseMessage := []byte("Hello, client!")
		receivedResponse := clientReadBuf[:n]
		if !bytes.Equal(receivedResponse, responseMessage) {
			t.Errorf("Client received %q, expected %q", receivedResponse, responseMessage)
		}
	}()

	// Server reads and writes response
	go func() {
		defer func() { commDone <- true }()

		// Server reads and decrypts message
		serverReadBuf := make([]byte, 1024)
		n, err := serverSecureConn.Read(serverReadBuf)
		if err != nil {
			t.Errorf("Server read failed: %v", err)
			return
		}

		receivedMessage := serverReadBuf[:n]
		if !bytes.Equal(receivedMessage, testMessage) {
			t.Errorf("Server received %q, expected %q", receivedMessage, testMessage)
		}

		// Server writes encrypted response
		responseMessage := []byte("Hello, client!")
		n, err = serverSecureConn.Write(responseMessage)
		if err != nil {
			t.Errorf("Server write failed: %v", err)
			return
		}
		if n != len(responseMessage) {
			t.Errorf("Server wrote %d bytes, expected %d", n, len(responseMessage))
		}
	}()

	// Wait for both communication operations to complete
	for i := 0; i < 2; i++ {
		select {
		case <-commDone:
			// One operation completed
		case <-time.After(5 * time.Second):
			t.Fatal("Communication timed out")
		}
	}

	// Close connections
	clientSecureConn.Close()
	serverSecureConn.Close()
}

// TestValidateIdentity tests the identity validation function
func TestValidateIdentity(t *testing.T) {
	// Create a valid credential
	cred, err := NewCredential()
	if err != nil {
		t.Fatalf("Failed to create credential: %v", err)
	}

	// Create a valid identity
	validIdentity := &rdsec.Identity{
		Id:        cred.ID(),
		PublicKey: cred.PublicKey(),
	}

	if !ValidateIdentity(validIdentity) {
		t.Error("Valid identity was rejected")
	}

	// Test invalid identity (wrong ID)
	invalidIdentity := &rdsec.Identity{
		Id:        "wrong-id",
		PublicKey: cred.PublicKey(),
	}

	if ValidateIdentity(invalidIdentity) {
		t.Error("Invalid identity was accepted")
	}

	// Test invalid identity (wrong public key)
	_, wrongPubKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate wrong public key: %v", err)
	}

	invalidIdentity2 := &rdsec.Identity{
		Id:        cred.ID(),
		PublicKey: wrongPubKey,
	}

	if ValidateIdentity(invalidIdentity2) {
		t.Error("Invalid identity with wrong public key was accepted")
	}
}

// TestCredential tests the credential functions
func TestCredential(t *testing.T) {
	// Test creating a new credential
	cred, err := NewCredential()
	if err != nil {
		t.Fatalf("Failed to create credential: %v", err)
	}

	// Test ID function
	id := cred.ID()
	if id == "" {
		t.Error("Credential ID is empty")
	}

	// Test public key function
	pubKey := cred.PublicKey()
	if len(pubKey) != ed25519.PublicKeySize {
		t.Errorf("Public key size is %d, expected %d", len(pubKey), ed25519.PublicKeySize)
	}

	// Test private key function
	privKey := cred.PrivateKey()
	if len(privKey) != ed25519.PrivateKeySize {
		t.Errorf("Private key size is %d, expected %d", len(privKey), ed25519.PrivateKeySize)
	}

	// Test sign and verify
	message := []byte("test message")
	signature := cred.Sign(message)

	if !cred.Verify(message, signature) {
		t.Error("Signature verification failed")
	}

	// Test verification with wrong message
	wrongMessage := []byte("wrong message")
	if cred.Verify(wrongMessage, signature) {
		t.Error("Signature verification should have failed for wrong message")
	}

	// Test verification with wrong signature
	wrongSignature := make([]byte, ed25519.SignatureSize)
	if cred.Verify(message, wrongSignature) {
		t.Error("Signature verification should have failed for wrong signature")
	}
}

// TestHandshakeWithInvalidALPN tests that handshake fails with different ALPNs
func TestHandshakeWithInvalidALPN(t *testing.T) {
	// Create credentials for client and server
	clientCred, err := NewCredential()
	if err != nil {
		t.Fatalf("Failed to create client credential: %v", err)
	}

	serverCred, err := NewCredential()
	if err != nil {
		t.Fatalf("Failed to create server credential: %v", err)
	}

	// Create handshakers
	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	// Create a pipe for bidirectional communication
	clientReader, clientWriter := io.Pipe()
	serverReader, serverWriter := io.Pipe()

	// Create pipe connections
	clientPipeConn := &pipeConn{
		reader: clientReader,
		writer: serverWriter,
	}
	serverPipeConn := &pipeConn{
		reader: serverReader,
		writer: clientWriter,
	}

	// Use different ALPNs
	clientALPN := "client-alpn"
	serverALPN := "server-alpn"

	// Use channels to coordinate
	done := make(chan bool, 2)
	var clientErr, serverErr error

	// Start server handshake in a goroutine
	go func() {
		defer func() { done <- true }()
		_, serverErr = serverHandshaker.ServerHandshake(serverPipeConn, serverALPN)
	}()

	// Give the server a moment to start waiting
	time.Sleep(10 * time.Millisecond)

	// Start client handshake in a goroutine
	go func() {
		defer func() { done <- true }()
		_, clientErr = clientHandshaker.ClientHandshake(clientPipeConn, clientALPN)
	}()

	// Wait for both handshakes to complete
	for i := 0; i < 2; i++ {
		select {
		case <-done:
			// One handshake completed
		case <-time.After(5 * time.Second):
			t.Fatal("Handshake timed out")
		}
	}

	// Check for errors
	if clientErr == nil {
		t.Error("Client handshake should have failed with different ALPNs")
	}
	if serverErr == nil {
		t.Error("Server handshake should have failed with different ALPNs")
	}
}
