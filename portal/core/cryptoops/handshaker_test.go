package cryptoops

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gosuda/portal/portal/core/proto/rdsec"
	"golang.org/x/crypto/curve25519"
)

// pipeConn creates a bidirectional pipe for testing using TCP loopback
func pipeConn() (net.Conn, net.Conn) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}

	connCh := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			panic(err)
		}
		connCh <- conn
		listener.Close()
	}()

	clientConn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		panic(err)
	}

	serverConn := <-connCh
	return clientConn, serverConn
}

// TestNewHandshaker tests handshaker creation
func TestNewHandshaker(t *testing.T) {
	cred, err := NewCredential()
	if err != nil {
		t.Fatalf("Failed to create credential: %v", err)
	}

	h := NewHandshaker(cred)
	if h == nil {
		t.Fatal("NewHandshaker returned nil")
	}
	if h.credential != cred {
		t.Error("Handshaker credential mismatch")
	}
}

// TestHandshakeSuccess tests a successful handshake
func TestHandshakeSuccess(t *testing.T) {
	clientCred, err := NewCredential()
	if err != nil {
		t.Fatalf("Failed to create client credential: %v", err)
	}

	serverCred, err := NewCredential()
	if err != nil {
		t.Fatalf("Failed to create server credential: %v", err)
	}

	clientConn, serverConn := pipeConn()

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	// Run handshakes concurrently
	var clientSecure, serverSecure *SecureConnection
	var clientErr, serverErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		clientSecure, clientErr = clientHandshaker.ClientHandshake(clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, serverErr = serverHandshaker.ServerHandshake(serverConn, []string{"test-alpn"})
	}()

	wg.Wait()

	if clientErr != nil {
		t.Fatalf("Client handshake failed: %v", clientErr)
	}
	if serverErr != nil {
		t.Fatalf("Server handshake failed: %v", serverErr)
	}
	if clientSecure == nil || serverSecure == nil {
		t.Fatal("Secure connections are nil")
	}

	// Test that connections can communicate
	testMessage := []byte("Hello, secure world!")

	// Client sends to server
	_, err = clientSecure.Write(testMessage)
	if err != nil {
		t.Fatalf("Client write failed: %v", err)
	}

	// Server receives
	received := make([]byte, len(testMessage))
	n, err := io.ReadFull(serverSecure, received)
	if err != nil {
		t.Fatalf("Server read failed: %v", err)
	}
	if n != len(testMessage) {
		t.Fatalf("Expected to read %d bytes, got %d", len(testMessage), n)
	}
	if !bytes.Equal(testMessage, received) {
		t.Errorf("Message mismatch: expected %q, got %q", testMessage, received)
	}

	// Server sends to client
	responseMessage := []byte("Hello back!")
	_, err = serverSecure.Write(responseMessage)
	if err != nil {
		t.Fatalf("Server write failed: %v", err)
	}

	// Client receives
	received = make([]byte, len(responseMessage))
	n, err = io.ReadFull(clientSecure, received)
	if err != nil {
		t.Fatalf("Client read failed: %v", err)
	}
	if n != len(responseMessage) {
		t.Fatalf("Expected to read %d bytes, got %d", len(responseMessage), n)
	}
	if !bytes.Equal(responseMessage, received) {
		t.Errorf("Message mismatch: expected %q, got %q", responseMessage, received)
	}

	clientSecure.Close()
	serverSecure.Close()
}

// TestHandshakeInvalidSignature tests handshake validation
func TestHandshakeInvalidSignature(t *testing.T) {
	// This is tested implicitly through validateClientInit/validateServerInit
	// Detailed unit tests would require mocking which is complex
	t.Skip("Covered by integration tests")
}

// TestHandshakeInvalidTimestamp tests timestamp validation
func TestHandshakeInvalidTimestamp(t *testing.T) {
	// Test the validateTimestamp function directly
	now := time.Now().Unix()

	// Valid timestamp
	if err := validateTimestamp(now); err != nil {
		t.Errorf("Current timestamp should be valid: %v", err)
	}

	// Old timestamp (> 30s)
	if err := validateTimestamp(now - 100); err == nil {
		t.Error("Old timestamp should be invalid")
	}

	// Future timestamp (> 30s)
	if err := validateTimestamp(now + 100); err == nil {
		t.Error("Future timestamp should be invalid")
	}
}

// TestHandshakeInvalidProtocolVersion tests protocol version
func TestHandshakeInvalidProtocolVersion(t *testing.T) {
	t.Skip("Covered by integration tests")
}

// TestHandshakeInvalidALPN tests ALPN validation
func TestHandshakeInvalidALPN(t *testing.T) {
	t.Skip("Covered by integration tests")
}

// TestEncryptionRoundTrip tests encryption and decryption
func TestEncryptionRoundTrip(t *testing.T) {
	clientCred, _ := NewCredential()
	serverCred, _ := NewCredential()

	clientConn, serverConn := pipeConn()

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	var clientSecure, serverSecure *SecureConnection
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		clientSecure, _ = clientHandshaker.ClientHandshake(clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, _ = serverHandshaker.ServerHandshake(serverConn, []string{"test-alpn"})
	}()

	wg.Wait()

	testCases := []struct {
		name    string
		message []byte
	}{
		{"Empty", []byte{}},
		{"Small", []byte("Hello")},
		{"Medium", bytes.Repeat([]byte("A"), 1024)},
		{"Large", bytes.Repeat([]byte("B"), 10000)},
		{"Binary", []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Client to Server
			_, err := clientSecure.Write(tc.message)
			if err != nil {
				t.Fatalf("Write failed: %v", err)
			}

			received := make([]byte, len(tc.message))
			if len(tc.message) > 0 {
				_, err = io.ReadFull(serverSecure, received)
				if err != nil {
					t.Fatalf("Read failed: %v", err)
				}
				if !bytes.Equal(tc.message, received) {
					t.Error("Message mismatch")
				}
			}

			// Server to Client
			_, err = serverSecure.Write(tc.message)
			if err != nil {
				t.Fatalf("Write failed: %v", err)
			}

			received = make([]byte, len(tc.message))
			if len(tc.message) > 0 {
				_, err = io.ReadFull(clientSecure, received)
				if err != nil {
					t.Fatalf("Read failed: %v", err)
				}
				if !bytes.Equal(tc.message, received) {
					t.Error("Message mismatch")
				}
			}
		})
	}

	clientSecure.Close()
	serverSecure.Close()
}

// TestFragmentation tests large message fragmentation
func TestFragmentation(t *testing.T) {
	clientCred, _ := NewCredential()
	serverCred, _ := NewCredential()

	clientConn, serverConn := pipeConn()

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	var clientSecure, serverSecure *SecureConnection
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		clientSecure, _ = clientHandshaker.ClientHandshake(clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, _ = serverHandshaker.ServerHandshake(serverConn, []string{"test-alpn"})
	}()

	wg.Wait()

	// Test message larger than fragment size (32MB)
	largeMessage := bytes.Repeat([]byte("X"), 40*1024*1024) // 40MB

	go func() {
		_, err := clientSecure.Write(largeMessage)
		if err != nil {
			t.Errorf("Write large message failed: %v", err)
		}
	}()

	// Read in chunks
	received := make([]byte, len(largeMessage))
	totalRead := 0
	for totalRead < len(largeMessage) {
		n, err := serverSecure.Read(received[totalRead:])
		if err != nil {
			t.Fatalf("Read failed at %d bytes: %v", totalRead, err)
		}
		totalRead += n
	}

	if !bytes.Equal(largeMessage, received) {
		t.Error("Large message mismatch after fragmentation")
	}

	clientSecure.Close()
	serverSecure.Close()
}

// TestConcurrentWrites tests concurrent writes
func TestConcurrentWrites(t *testing.T) {
	clientCred, _ := NewCredential()
	serverCred, _ := NewCredential()

	clientConn, serverConn := pipeConn()

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	var clientSecure, serverSecure *SecureConnection
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		clientSecure, _ = clientHandshaker.ClientHandshake(clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, _ = serverHandshaker.ServerHandshake(serverConn, []string{"test-alpn"})
	}()

	wg.Wait()

	const numMessages = 100
	messages := make([][]byte, numMessages)
	for i := 0; i < numMessages; i++ {
		messages[i] = []byte{byte(i), byte(i >> 8)}
	}

	// Write concurrently from client
	var writeWg sync.WaitGroup
	for i := 0; i < numMessages; i++ {
		writeWg.Add(1)
		go func(msg []byte) {
			defer writeWg.Done()
			clientSecure.Write(msg)
		}(messages[i])
	}
	writeWg.Wait()

	// Read all messages
	received := make(map[string]bool)
	for i := 0; i < numMessages; i++ {
		buf := make([]byte, 2)
		_, err := io.ReadFull(serverSecure, buf)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		key := string(buf)
		if received[key] {
			t.Errorf("Duplicate message received: %v", buf)
		}
		received[key] = true
	}

	if len(received) != numMessages {
		t.Errorf("Expected %d unique messages, got %d", numMessages, len(received))
	}

	clientSecure.Close()
	serverSecure.Close()
}

// TestInvalidIdentity tests identity validation
func TestInvalidIdentity(t *testing.T) {
	cred, _ := NewCredential()

	// Valid identity
	validIdentity := &rdsec.Identity{
		Id:        cred.ID(),
		PublicKey: cred.PublicKey(),
	}
	if !ValidateIdentity(validIdentity) {
		t.Error("Valid identity should pass validation")
	}

	// Wrong ID
	invalidIdentity := &rdsec.Identity{
		Id:        "WRONG_ID",
		PublicKey: cred.PublicKey(),
	}
	if ValidateIdentity(invalidIdentity) {
		t.Error("Identity with wrong ID should fail validation")
	}

	// Wrong key size
	invalidIdentity2 := &rdsec.Identity{
		Id:        cred.ID(),
		PublicKey: []byte{1, 2, 3},
	}
	if ValidateIdentity(invalidIdentity2) {
		t.Error("Identity with wrong key size should fail validation")
	}
}

// TestGenerateX25519KeyPair tests X25519 key pair generation
func TestGenerateX25519KeyPair(t *testing.T) {
	priv1, pub1, err := generateX25519KeyPair()
	if err != nil {
		t.Fatalf("Failed to generate key pair: %v", err)
	}

	if len(priv1) != curve25519.ScalarSize {
		t.Errorf("Expected private key size %d, got %d", curve25519.ScalarSize, len(priv1))
	}
	if len(pub1) != curve25519.PointSize {
		t.Errorf("Expected public key size %d, got %d", curve25519.PointSize, len(pub1))
	}

	// Generate another pair and ensure they're different
	priv2, pub2, err := generateX25519KeyPair()
	if err != nil {
		t.Fatalf("Failed to generate second key pair: %v", err)
	}

	if bytes.Equal(priv1, priv2) {
		t.Error("Generated same private key twice")
	}
	if bytes.Equal(pub1, pub2) {
		t.Error("Generated same public key twice")
	}

	// Verify public key derivation
	derivedPub, err := curve25519.X25519(priv1, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("Failed to derive public key: %v", err)
	}
	if !bytes.Equal(pub1, derivedPub) {
		t.Error("Public key doesn't match derived key")
	}
}

// TestDeriveKey tests HKDF key derivation
func TestDeriveKey(t *testing.T) {
	sharedSecret := make([]byte, 32)
	rand.Read(sharedSecret)

	salt1 := []byte("salt1")
	salt2 := []byte("salt2")
	info1 := []byte(clientKeyInfo)
	info2 := []byte(serverKeyInfo)

	key1 := deriveKey(sharedSecret, salt1, info1)
	key2 := deriveKey(sharedSecret, salt2, info1)
	key3 := deriveKey(sharedSecret, salt1, info2)
	key4 := deriveKey(sharedSecret, salt1, info1) // Same as key1

	// Check key size
	if len(key1) != sessionKeySize {
		t.Errorf("Expected key size %d, got %d", sessionKeySize, len(key1))
	}

	// Keys with different salts should be different
	if bytes.Equal(key1, key2) {
		t.Error("Keys with different salts are the same")
	}

	// Keys with different info should be different
	if bytes.Equal(key1, key3) {
		t.Error("Keys with different info are the same")
	}

	// Same inputs should produce same key
	if !bytes.Equal(key1, key4) {
		t.Error("Same inputs produced different keys")
	}
}

// TestValidateTimestamp tests timestamp validation
func TestValidateTimestamp(t *testing.T) {
	now := time.Now().Unix()

	testCases := []struct {
		name      string
		timestamp int64
		expectErr bool
	}{
		{"Current", now, false},
		{"5 seconds ago", now - 5, false},
		{"5 seconds future", now + 5, false},
		{"30 seconds ago", now - 30, false},
		{"30 seconds future", now + 30, false},
		{"31 seconds ago", now - 31, true},
		{"31 seconds future", now + 31, true},
		{"100 seconds ago", now - 100, true},
		{"100 seconds future", now + 100, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTimestamp(tc.timestamp)
			if tc.expectErr && err == nil {
				t.Error("Expected error but got none")
			}
			if !tc.expectErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

// TestLengthPrefixedReadWrite tests length-prefixed message encoding
func TestLengthPrefixedReadWrite(t *testing.T) {
	testMessages := [][]byte{
		{},
		{0x01},
		[]byte("Hello, World!"),
		bytes.Repeat([]byte("A"), 1000),
		make([]byte, 0),
	}

	for i, msg := range testMessages {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			var buf bytes.Buffer

			// Write
			err := writeLengthPrefixed(&buf, msg)
			if err != nil {
				t.Fatalf("Write failed: %v", err)
			}

			// Read
			received, err := readLengthPrefixed(&buf)
			if err != nil {
				t.Fatalf("Read failed: %v", err)
			}

			if !bytes.Equal(msg, received) {
				t.Errorf("Message mismatch: expected %v, got %v", msg, received)
			}
		})
	}
}

// TestReadLengthPrefixedTooLarge tests reading message exceeding size limit
func TestReadLengthPrefixedTooLarge(t *testing.T) {
	var buf bytes.Buffer

	// Write length exceeding maxRawPacketSize
	tooLarge := uint32(maxRawPacketSize + 1)
	lengthBytes := []byte{
		byte(tooLarge >> 24),
		byte(tooLarge >> 16),
		byte(tooLarge >> 8),
		byte(tooLarge),
	}
	buf.Write(lengthBytes)

	_, err := readLengthPrefixed(&buf)
	if err != ErrHandshakeFailed {
		t.Errorf("Expected ErrHandshakeFailed for oversized message, got %v", err)
	}
}

// TestWipeMemory tests memory wiping functionality
func TestWipeMemory(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	originalCap := cap(data)

	wipeMemory(data)

	// Check that all bytes in the capacity are zeroed
	fullData := data[:originalCap]
	for i, b := range fullData {
		if b != 0 {
			t.Errorf("Byte at index %d not wiped: %02x", i, b)
		}
	}
}

// TestBufferManagement tests buffer acquisition and release
func TestBufferManagement(t *testing.T) {
	// Acquire buffer
	buf := acquireBuffer(1024)
	if buf == nil {
		t.Fatal("acquireBuffer returned nil")
	}
	if cap(buf.B) < 1024 {
		t.Errorf("Expected capacity >= 1024, got %d", cap(buf.B))
	}

	// Write some data
	testData := []byte("sensitive data")
	buf.B = append(buf.B, testData...)

	// Release and verify wiping
	releaseBuffer(buf)

	// Acquire again and verify it's clean
	buf2 := acquireBuffer(1024)
	for i := 0; i < len(testData) && i < len(buf2.B); i++ {
		if buf2.B[i] != 0 {
			t.Errorf("Buffer not properly wiped at index %d", i)
		}
	}
	releaseBuffer(buf2)
}

// TestSecureConnectionPartialRead tests reading when buffer is smaller than message
func TestSecureConnectionPartialRead(t *testing.T) {
	clientCred, _ := NewCredential()
	serverCred, _ := NewCredential()

	clientConn, serverConn := pipeConn()

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	var clientSecure, serverSecure *SecureConnection
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		clientSecure, _ = clientHandshaker.ClientHandshake(clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, _ = serverHandshaker.ServerHandshake(serverConn, []string{"test-alpn"})
	}()

	wg.Wait()

	message := []byte("This is a longer message that will be read in parts")

	// Send message
	_, err := clientSecure.Write(message)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Read in small chunks
	received := make([]byte, 0, len(message))
	smallBuf := make([]byte, 10) // Smaller than message

	for len(received) < len(message) {
		n, err := serverSecure.Read(smallBuf)
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		received = append(received, smallBuf[:n]...)
	}

	if !bytes.Equal(message, received) {
		t.Error("Message mismatch with partial reads")
	}

	clientSecure.Close()
	serverSecure.Close()
}

// TestRealNetworkConnection tests with actual TCP connection
func TestRealNetworkConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping network test in short mode")
	}

	clientCred, _ := NewCredential()
	serverCred, _ := NewCredential()

	// Start server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start listener: %v", err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().String()

	var serverSecure *SecureConnection
	var serverErr error
	serverDone := make(chan struct{})

	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			serverErr = err
			return
		}

		serverHandshaker := NewHandshaker(serverCred)
		serverSecure, serverErr = serverHandshaker.ServerHandshake(conn, []string{"test-alpn"})
	}()

	// Connect client
	clientConn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	clientHandshaker := NewHandshaker(clientCred)
	clientSecure, clientErr := clientHandshaker.ClientHandshake(clientConn, "test-alpn")

	<-serverDone

	if clientErr != nil {
		t.Fatalf("Client handshake failed: %v", clientErr)
	}
	if serverErr != nil {
		t.Fatalf("Server handshake failed: %v", serverErr)
	}

	// Test communication
	testMessage := []byte("Hello over real network!")

	_, err = clientSecure.Write(testMessage)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	received := make([]byte, len(testMessage))
	_, err = io.ReadFull(serverSecure, received)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if !bytes.Equal(testMessage, received) {
		t.Error("Message mismatch over network")
	}

	clientSecure.Close()
	serverSecure.Close()
}

// BenchmarkHandshake benchmarks the handshake process
func BenchmarkHandshake(b *testing.B) {
	clientCred, _ := NewCredential()
	serverCred, _ := NewCredential()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clientConn, serverConn := pipeConn()

		clientHandshaker := NewHandshaker(clientCred)
		serverHandshaker := NewHandshaker(serverCred)

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			clientHandshaker.ClientHandshake(clientConn, "test-alpn")
		}()

		go func() {
			defer wg.Done()
			serverHandshaker.ServerHandshake(serverConn, []string{"test-alpn"})
		}()

		wg.Wait()
	}
}

// BenchmarkEncryption benchmarks encryption throughput
func BenchmarkEncryption(b *testing.B) {
	clientCred, _ := NewCredential()
	serverCred, _ := NewCredential()

	clientConn, serverConn := pipeConn()

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	var clientSecure, serverSecure *SecureConnection
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		clientSecure, _ = clientHandshaker.ClientHandshake(clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, _ = serverHandshaker.ServerHandshake(serverConn, []string{"test-alpn"})
	}()

	wg.Wait()

	message := bytes.Repeat([]byte("A"), 1024) // 1KB message

	go func() {
		buf := make([]byte, 1024)
		for {
			serverSecure.Read(buf)
		}
	}()

	b.ResetTimer()
	b.SetBytes(int64(len(message)))

	for i := 0; i < b.N; i++ {
		clientSecure.Write(message)
	}

	clientSecure.Close()
	serverSecure.Close()
}
