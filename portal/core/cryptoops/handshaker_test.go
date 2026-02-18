package cryptoops

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pipeConn creates a bidirectional pipe for testing using TCP loopback.
func pipeConn() (clientConn, serverConn net.Conn) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}

	connCh := make(chan net.Conn, 1)
	go func() {
		acceptedConn, acceptErr := listener.Accept()
		if acceptErr != nil {
			panic(acceptErr)
		}
		connCh <- acceptedConn
		listener.Close()
	}()

	clientConn, err = net.Dial("tcp", listener.Addr().String())
	if err != nil {
		panic(err)
	}

	serverConn = <-connCh
	return clientConn, serverConn
}

type shortWriter struct {
	writer   io.Writer
	maxChunk int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	n := w.maxChunk
	if n <= 0 || n > len(p) {
		n = len(p)
	}

	return w.writer.Write(p[:n])
}

type shortWriteReadWriteCloser struct {
	io.ReadWriteCloser
	maxChunk int
}

func (c *shortWriteReadWriteCloser) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	n := c.maxChunk
	if n <= 0 || n > len(p) {
		n = len(p)
	}

	return c.ReadWriteCloser.Write(p[:n])
}

// TestNewHandshaker tests handshaker creation.
func TestNewHandshaker(t *testing.T) {
	cred, err := NewCredential()
	require.NoError(t, err, "Failed to create credential")

	h := NewHandshaker(cred)
	require.NotNil(t, h, "NewHandshaker returned nil")
	assert.Equal(t, cred, h.credential, "Handshaker credential mismatch")
}

// TestHandshakeSuccess tests a successful handshake.
func TestHandshakeSuccess(t *testing.T) {
	clientCred, err := NewCredential()
	require.NoError(t, err, "Failed to create client credential")

	serverCred, err := NewCredential()
	require.NoError(t, err, "Failed to create server credential")

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
		clientSecure, clientErr = clientHandshaker.ClientHandshake(context.Background(), clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, serverErr = serverHandshaker.ServerHandshake(context.Background(), serverConn, []string{"test-alpn"})
	}()

	wg.Wait()

	require.NoError(t, clientErr, "Client handshake failed")
	require.NoError(t, serverErr, "Server handshake failed")
	require.NotNil(t, clientSecure, "Client secure connection is nil")
	require.NotNil(t, serverSecure, "Server secure connection is nil")
	assert.NotEmpty(t, clientSecure.LocalID(), "Client local ID should not be empty")
	assert.NotEmpty(t, clientSecure.RemoteID(), "Client remote ID should not be empty")
	assert.NotEmpty(t, serverSecure.LocalID(), "Server local ID should not be empty")
	assert.NotEmpty(t, serverSecure.RemoteID(), "Server remote ID should not be empty")
	assert.Equal(t, clientSecure.LocalID(), serverSecure.RemoteID(), "Client local ID should match server remote ID")
	assert.Equal(t, serverSecure.LocalID(), clientSecure.RemoteID(), "Server local ID should match client remote ID")

	// Test that connections can communicate
	testMessage := []byte("Hello, secure world!")

	// Client sends to server
	_, err = clientSecure.Write(testMessage)
	require.NoError(t, err, "Client write failed")

	// Server receives
	received := make([]byte, len(testMessage))
	n, err := io.ReadFull(serverSecure, received)
	require.NoError(t, err, "Server read failed")
	assert.Equal(t, len(testMessage), n, "Expected to read %d bytes, got %d", len(testMessage), n)
	assert.Equal(t, testMessage, received, "Message mismatch")

	// Server sends to client
	responseMessage := []byte("Hello back!")
	_, err = serverSecure.Write(responseMessage)
	require.NoError(t, err, "Server write failed")

	// Client receives
	received = make([]byte, len(responseMessage))
	n, err = io.ReadFull(clientSecure, received)
	require.NoError(t, err, "Client read failed")
	assert.Equal(t, len(responseMessage), n, "Expected to read %d bytes, got %d", len(responseMessage), n)
	assert.Equal(t, responseMessage, received, "Message mismatch")

	clientSecure.Close()
	serverSecure.Close()
}

// TestHandshakeALPNMismatch tests that mismatched ALPN causes handshake failure.
func TestHandshakeALPNMismatch(t *testing.T) {
	clientCred, err := NewCredential()
	require.NoError(t, err)
	serverCred, err := NewCredential()
	require.NoError(t, err)

	clientConn, serverConn := pipeConn()

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	var serverErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = clientHandshaker.ClientHandshake(context.Background(), clientConn, "alpn-a")
	}()

	go func() {
		defer wg.Done()
		_, serverErr = serverHandshaker.ServerHandshake(context.Background(), serverConn, []string{"alpn-b"})
	}()

	wg.Wait()

	require.Error(t, serverErr, "Expected server handshake to fail with ALPN mismatch")
}

// TestEncryptionRoundTrip tests encryption and decryption.
func TestEncryptionRoundTrip(t *testing.T) {
	clientCred, err := NewCredential()
	require.NoError(t, err)
	serverCred, err := NewCredential()
	require.NoError(t, err)

	clientConn, serverConn := pipeConn()

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	var clientSecure, serverSecure *SecureConnection
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		clientSecure, _ = clientHandshaker.ClientHandshake(context.Background(), clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, _ = serverHandshaker.ServerHandshake(context.Background(), serverConn, []string{"test-alpn"})
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
			require.NoError(t, err, "Write failed")

			received := make([]byte, len(tc.message))
			if len(tc.message) > 0 {
				_, err = io.ReadFull(serverSecure, received)
				require.NoError(t, err, "Read failed")
				assert.Equal(t, tc.message, received, "Message mismatch")
			}

			// Server to Client
			_, err = serverSecure.Write(tc.message)
			require.NoError(t, err, "Write failed")

			received = make([]byte, len(tc.message))
			if len(tc.message) > 0 {
				_, err = io.ReadFull(clientSecure, received)
				require.NoError(t, err, "Read failed")
				assert.Equal(t, tc.message, received, "Message mismatch")
			}
		})
	}

	clientSecure.Close()
	serverSecure.Close()
}

// TestFragmentation tests large message fragmentation.
func TestFragmentation(t *testing.T) {
	clientCred, err := NewCredential()
	require.NoError(t, err)
	serverCred, err := NewCredential()
	require.NoError(t, err)

	clientConn, serverConn := pipeConn()

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	var clientSecure, serverSecure *SecureConnection
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		clientSecure, _ = clientHandshaker.ClientHandshake(context.Background(), clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, _ = serverHandshaker.ServerHandshake(context.Background(), serverConn, []string{"test-alpn"})
	}()

	wg.Wait()

	// Test message larger than fragment size (32MB)
	largeMessage := bytes.Repeat([]byte("X"), 40*1024*1024) // 40MB

	go func() {
		_, err := clientSecure.Write(largeMessage)
		require.NoError(t, err, "Write large message failed")
	}()

	// Read in chunks
	received := make([]byte, len(largeMessage))
	totalRead := 0
	for totalRead < len(largeMessage) {
		n, err := serverSecure.Read(received[totalRead:])
		require.NoError(t, err, "Read failed at %d bytes", totalRead)
		totalRead += n
	}

	assert.Equal(t, largeMessage, received, "Large message mismatch after fragmentation")

	clientSecure.Close()
	serverSecure.Close()
}

func TestSecureConnectionWriteShortWriteRegression(t *testing.T) {
	clientCred, err := NewCredential()
	require.NoError(t, err)
	serverCred, err := NewCredential()
	require.NoError(t, err)

	clientConn, serverConn := pipeConn()

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	var clientSecure, serverSecure *SecureConnection
	var clientErr, serverErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		clientSecure, clientErr = clientHandshaker.ClientHandshake(context.Background(), clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, serverErr = serverHandshaker.ServerHandshake(context.Background(), serverConn, []string{"test-alpn"})
	}()

	wg.Wait()

	require.NoError(t, clientErr, "Client handshake failed")
	require.NoError(t, serverErr, "Server handshake failed")

	clientSecure.conn = &shortWriteReadWriteCloser{
		ReadWriteCloser: clientSecure.conn,
		maxChunk:        5,
	}

	message := bytes.Repeat([]byte("Z"), 4096)
	_, err = clientSecure.Write(message)
	require.NoError(t, err, "Client write failed")

	err = serverSecure.SetReadDeadline(time.Now().Add(2 * time.Second))
	require.NoError(t, err, "SetReadDeadline failed")
	defer serverSecure.SetReadDeadline(time.Time{})

	received := make([]byte, len(message))
	n, err := io.ReadFull(serverSecure, received)
	require.NoError(t, err, "Server read failed")
	assert.Equal(t, len(message), n, "Expected to read %d bytes, got %d", len(message), n)
	assert.Equal(t, message, received, "Message mismatch after short writes")

	_ = clientSecure.Close()
	_ = serverSecure.Close()
}

// TestConcurrentWrites tests concurrent writes.
func TestConcurrentWrites(t *testing.T) {
	clientCred, err := NewCredential()
	require.NoError(t, err)
	serverCred, err := NewCredential()
	require.NoError(t, err)

	clientConn, serverConn := pipeConn()

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	var clientSecure, serverSecure *SecureConnection
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		clientSecure, _ = clientHandshaker.ClientHandshake(context.Background(), clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, _ = serverHandshaker.ServerHandshake(context.Background(), serverConn, []string{"test-alpn"})
	}()

	wg.Wait()

	const numMessages = 100
	messages := make([][]byte, numMessages)
	for i := range numMessages {
		messages[i] = []byte{byte(i), byte(i >> 8)}
	}

	// Write concurrently from client
	var writeWg sync.WaitGroup
	for i := range numMessages {
		writeWg.Add(1)
		go func(msg []byte) {
			defer writeWg.Done()
			clientSecure.Write(msg)
		}(messages[i])
	}
	writeWg.Wait()

	// Read all messages
	received := make(map[string]bool)
	for range numMessages {
		buf := make([]byte, 2)
		_, err := io.ReadFull(serverSecure, buf)
		require.NoError(t, err, "Read failed")
		key := string(buf)
		assert.False(t, received[key], "Duplicate message received: %v", buf)
		received[key] = true
	}

	assert.Len(t, received, numMessages, "Expected %d unique messages, got %d", numMessages, len(received))

	clientSecure.Close()
	serverSecure.Close()
}

// TestX25519KeyDerivation tests X25519 key generation behavior.
func TestX25519KeyDerivation(t *testing.T) {
	cred1, err := NewCredential()
	require.NoError(t, err)
	cred2, err := NewCredential()
	require.NoError(t, err)

	priv1 := cred1.X25519PrivateKey()
	pub1 := cred1.X25519PublicKey()
	priv2 := cred2.X25519PrivateKey()
	pub2 := cred2.X25519PublicKey()

	// Key sizes
	assert.Len(t, priv1, 32, "Expected X25519 private key size 32, got %d", len(priv1))
	assert.Len(t, pub1, 32, "Expected X25519 public key size 32, got %d", len(pub1))

	// Different credentials produce different keys
	assert.NotEqual(t, priv1, priv2, "Different credentials produced same X25519 private key")
	assert.NotEqual(t, pub1, pub2, "Different credentials produced same X25519 public key")

	// Deterministic: same credential always produces same key
	assert.Equal(t, priv1, cred1.X25519PrivateKey(), "X25519PrivateKey not deterministic")
	assert.Equal(t, pub1, cred1.X25519PublicKey(), "X25519PublicKey not deterministic")
}

// TestLengthPrefixedReadWrite tests length-prefixed message encoding.
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
			require.NoError(t, err, "Write failed")

			// Read
			received, err := readLengthPrefixed(&buf)
			require.NoError(t, err, "Read failed")

			assert.Equal(t, msg, received, "Message mismatch")
		})
	}
}

func TestLengthPrefixedReadWriteShortWriteRegression(t *testing.T) {
	message := bytes.Repeat([]byte("S"), 2048)

	var buf bytes.Buffer
	writer := &shortWriter{
		writer:   &buf,
		maxChunk: 3,
	}

	err := writeLengthPrefixed(writer, message)
	require.NoError(t, err, "Write failed")

	received, err := readLengthPrefixed(&buf)
	require.NoError(t, err, "Read failed")

	assert.Equal(t, message, received, "Message mismatch after short writes")
}

// TestReadLengthPrefixedTooLarge tests reading message exceeding size limit.
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
	assert.ErrorIs(t, err, ErrHandshakeFailed, "Expected ErrHandshakeFailed for oversized message")
}

// TestWipeMemory tests memory wiping functionality.
func TestWipeMemory(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	originalCap := cap(data)

	wipeMemory(data)

	// Check that all bytes in the capacity are zeroed
	fullData := data[:originalCap]
	for i, b := range fullData {
		assert.Zero(t, b, "Byte at index %d not wiped: %02x", i, b)
	}
}

// TestBufferManagement tests buffer acquisition and release.
func TestBufferManagement(t *testing.T) {
	// Acquire buffer
	buf := acquireBuffer(1024)
	require.NotNil(t, buf, "acquireBuffer returned nil")
	assert.GreaterOrEqual(t, cap(buf.B), 1024, "Expected capacity >= 1024, got %d", cap(buf.B))

	// Write some data
	testData := []byte("sensitive data")
	buf.B = append(buf.B, testData...)

	// Release and verify wiping
	releaseBuffer(buf)

	// Acquire again and verify it's clean
	buf2 := acquireBuffer(1024)
	for i := 0; i < len(testData) && i < len(buf2.B); i++ {
		assert.Zero(t, buf2.B[i], "Buffer not properly wiped at index %d", i)
	}
	releaseBuffer(buf2)
}

// TestSecureConnectionPartialRead tests reading when buffer is smaller than message.
func TestSecureConnectionPartialRead(t *testing.T) {
	clientCred, err := NewCredential()
	require.NoError(t, err)
	serverCred, err := NewCredential()
	require.NoError(t, err)

	clientConn, serverConn := pipeConn()

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	var clientSecure, serverSecure *SecureConnection
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		clientSecure, _ = clientHandshaker.ClientHandshake(context.Background(), clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, _ = serverHandshaker.ServerHandshake(context.Background(), serverConn, []string{"test-alpn"})
	}()

	wg.Wait()

	message := []byte("This is a longer message that will be read in parts")

	// Send message
	_, err = clientSecure.Write(message)
	require.NoError(t, err, "Write failed")

	// Read in small chunks
	received := make([]byte, 0, len(message))
	smallBuf := make([]byte, 10) // Smaller than message

	for len(received) < len(message) {
		n, readErr := serverSecure.Read(smallBuf)
		require.NoError(t, readErr, "Read failed")
		received = append(received, smallBuf[:n]...)
	}

	assert.Equal(t, message, received, "Message mismatch with partial reads")

	clientSecure.Close()
	serverSecure.Close()
}

// TestRealNetworkConnection tests with actual TCP connection.
func TestRealNetworkConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping network test in short mode")
	}

	clientCred, err := NewCredential()
	require.NoError(t, err)
	serverCred, err := NewCredential()
	require.NoError(t, err)

	// Start server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "Failed to start listener")
	defer listener.Close()

	serverAddr := listener.Addr().String()

	var serverSecure *SecureConnection
	var serverErr error
	serverDone := make(chan struct{})

	go func() {
		defer close(serverDone)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverErr = acceptErr
			return
		}

		serverHandshaker := NewHandshaker(serverCred)
		serverSecure, serverErr = serverHandshaker.ServerHandshake(context.Background(), conn, []string{"test-alpn"})
	}()

	// Connect client
	clientConn, err := net.Dial("tcp", serverAddr)
	require.NoError(t, err, "Failed to connect")

	clientHandshaker := NewHandshaker(clientCred)
	clientSecure, clientErr := clientHandshaker.ClientHandshake(context.Background(), clientConn, "test-alpn")

	<-serverDone

	require.NoError(t, clientErr, "Client handshake failed")
	require.NoError(t, serverErr, "Server handshake failed")

	// Test communication
	testMessage := []byte("Hello over real network!")

	_, err = clientSecure.Write(testMessage)
	require.NoError(t, err, "Write failed")

	received := make([]byte, len(testMessage))
	_, err = io.ReadFull(serverSecure, received)
	require.NoError(t, err, "Read failed")

	assert.Equal(t, testMessage, received, "Message mismatch over network")

	clientSecure.Close()
	serverSecure.Close()
}

// BenchmarkHandshake benchmarks the handshake process.
func BenchmarkHandshake(b *testing.B) {
	clientCred, _ := NewCredential()
	serverCred, _ := NewCredential()

	b.ResetTimer()
	for range b.N {
		clientConn, serverConn := pipeConn()

		clientHandshaker := NewHandshaker(clientCred)
		serverHandshaker := NewHandshaker(serverCred)

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			clientHandshaker.ClientHandshake(context.Background(), clientConn, "test-alpn")
		}()

		go func() {
			defer wg.Done()
			serverHandshaker.ServerHandshake(context.Background(), serverConn, []string{"test-alpn"})
		}()

		wg.Wait()
	}
}

// BenchmarkEncryption benchmarks encryption throughput.
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
		clientSecure, _ = clientHandshaker.ClientHandshake(context.Background(), clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, _ = serverHandshaker.ServerHandshake(context.Background(), serverConn, []string{"test-alpn"})
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

	for range b.N {
		clientSecure.Write(message)
	}

	clientSecure.Close()
	serverSecure.Close()
}

// TestConcurrentReadClose tests that closing the connection while reading is safe.
func TestConcurrentReadClose(t *testing.T) {
	clientCred, err := NewCredential()
	require.NoError(t, err)
	serverCred, err := NewCredential()
	require.NoError(t, err)

	clientConn, serverConn := pipeConn()

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	var clientSecure, serverSecure *SecureConnection
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		clientSecure, _ = clientHandshaker.ClientHandshake(context.Background(), clientConn, "test-alpn")
	}()

	go func() {
		defer wg.Done()
		serverSecure, _ = serverHandshaker.ServerHandshake(context.Background(), serverConn, []string{"test-alpn"})
	}()

	wg.Wait()

	// Start a goroutine that reads continuously
	readErrCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		_, err := clientSecure.Read(buf)
		readErrCh <- err
	}()

	// Give the reader a moment to start and block
	time.Sleep(10 * time.Millisecond)

	// Close the connection
	clientSecure.Close()

	// Check the read error
	select {
	case err := <-readErrCh:
		assert.Error(t, err, "Expected error from Read after Close")
		// We expect either net.ErrClosed or an IO error depending on timing
	case <-time.After(1 * time.Second):
		t.Error("Read did not return after Close")
	}

	serverSecure.Close()
}
