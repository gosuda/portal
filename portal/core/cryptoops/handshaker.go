package cryptoops

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/flynn/noise"
	"github.com/valyala/bytebufferpool"
)

func wipeMemory(b []byte) {
	b = b[:cap(b)]
	for i := range b {
		b[i] = 0
	}
}

func bufferGrow(buffer *bytebufferpool.ByteBuffer, n int) {
	currentCap := cap(buffer.B)
	if n > currentCap {
		wipeMemory(buffer.B)
		// Align to 16KB boundaries
		newSize := (n + 16383) &^ 16383
		buffer.B = make([]byte, 0, newSize)
	}
	buffer.B = buffer.B[:0]
}

func acquireBuffer(n int) *bytebufferpool.ByteBuffer {
	buffer := bytebufferpool.Get()
	if buffer.B == nil {
		buffer.B = make([]byte, 0)
	}
	bufferGrow(buffer, n)
	return buffer
}

func releaseBuffer(buffer *bytebufferpool.ByteBuffer) {
	wipeMemory(buffer.B)
	bytebufferpool.Put(buffer)
}

var (
	ErrHandshakeFailed  = errors.New("handshake failed")
	ErrEncryptionFailed = errors.New("encryption failed")
	ErrDecryptionFailed = errors.New("decryption failed")
)

const (
	noiseTagSize     = 16      // ChaCha20-Poly1305 authentication tag
	maxRawPacketSize = 1 << 26 // 64MB — same as relay server

	// noisePrologue binds the handshake to this specific protocol version.
	noisePrologue = "portal/noise/1"
)

func newNoiseCipherSuite() noise.CipherSuite {
	return noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)
}

// Handshaker handles the Noise Protocol Framework based handshake.
//
// Uses the XX pattern (Noise_XX_25519_ChaChaPoly_BLAKE2s) which provides:
//   - Mutual authentication via X25519 static keys
//   - Forward secrecy via ephemeral X25519 keys
//   - Identity hiding (static keys are encrypted)
type Handshaker struct {
	credential *Credential
}

// NewHandshaker creates a new Handshaker with the given credential.
func NewHandshaker(credential *Credential) *Handshaker {
	return &Handshaker{
		credential: credential,
	}
}

// SecureConnection represents a secured connection with authenticated encryption.
// Frames are length-prefixed: [4B big-endian length][ciphertext + 16B tag].
// Nonces are managed internally by the Noise CipherState (counter-based).
//
// Writes are serialized via writeMu because the CipherState uses sequential nonces.
// Reads are naturally serialized by the io.ReadFull blocking pattern.
type SecureConnection struct {
	conn io.ReadWriteCloser

	localID  string
	remoteID string

	encryptor *noise.CipherState
	decryptor *noise.CipherState

	writeMu    sync.Mutex // serializes writes (counter nonces require ordering)
	readBuffer *bytebufferpool.ByteBuffer

	// Ensure Close is safe and idempotent
	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func (sc *SecureConnection) SetDeadline(t time.Time) error {
	if conn, ok := sc.conn.(interface{ SetDeadline(time.Time) error }); ok {
		return conn.SetDeadline(t)
	}
	return nil
}

func (sc *SecureConnection) SetReadDeadline(t time.Time) error {
	if conn, ok := sc.conn.(interface{ SetReadDeadline(time.Time) error }); ok {
		return conn.SetReadDeadline(t)
	}
	return nil
}

func (sc *SecureConnection) SetWriteDeadline(t time.Time) error {
	if conn, ok := sc.conn.(interface{ SetWriteDeadline(time.Time) error }); ok {
		return conn.SetWriteDeadline(t)
	}
	return nil
}

func (sc *SecureConnection) LocalID() string {
	return sc.localID
}

func (sc *SecureConnection) RemoteID() string {
	return sc.remoteID
}

// Write encrypts and writes data to the underlying connection.
// Large messages are fragmented to stay within the maximum packet size.
func (sc *SecureConnection) Write(p []byte) (int, error) {
	sc.mu.RLock()
	if sc.closed {
		sc.mu.RUnlock()
		return 0, net.ErrClosed
	}
	sc.mu.RUnlock()

	const fragSize = maxRawPacketSize / 2
	if len(p) > fragSize {
		numFrags := (len(p) + fragSize - 1) / fragSize
		for i := range numFrags {
			start := i * fragSize
			end := min(start+fragSize, len(p))
			_, err := sc.writeFragment(p[start:end])
			if err != nil {
				return 0, err
			}
		}
		return len(p), nil
	}
	return sc.writeFragment(p)
}

// writeFragment encrypts and writes a single fragment.
// Must hold writeMu to ensure sequential nonces.
func (sc *SecureConnection) writeFragment(p []byte) (int, error) {
	sc.writeMu.Lock()
	defer sc.writeMu.Unlock()

	cipherSize := len(p) + noiseTagSize
	if cipherSize > maxRawPacketSize {
		return 0, fmt.Errorf("%w: ciphertext too large: %d", ErrEncryptionFailed, cipherSize)
	}
	bufferSize := 4 + cipherSize
	buffer := acquireBuffer(bufferSize)
	buffer.B = buffer.B[:4]
	defer releaseBuffer(buffer)

	buffer.B[0] = byte(cipherSize >> 24)
	buffer.B[1] = byte(cipherSize >> 16)
	buffer.B[2] = byte(cipherSize >> 8)
	buffer.B[3] = byte(cipherSize)

	var err error
	buffer.B, err = sc.encryptor.Encrypt(buffer.B, nil, p)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrEncryptionFailed, err)
	}

	err = writeFull(sc.conn, buffer.B)
	if err != nil {
		return 0, err
	}

	return len(p), nil
}

// Read reads and decrypts data from the underlying connection.
func (sc *SecureConnection) Read(p []byte) (int, error) {
	sc.mu.Lock()
	if sc.closed {
		sc.mu.Unlock()
		return 0, net.ErrClosed
	}

	if sc.readBuffer != nil && len(sc.readBuffer.B) > 0 {
		n := copy(p, sc.readBuffer.B)
		copy(sc.readBuffer.B[:len(sc.readBuffer.B)-n], sc.readBuffer.B[n:])
		sc.readBuffer.B = sc.readBuffer.B[:len(sc.readBuffer.B)-n]
		sc.mu.Unlock()
		return n, nil
	}
	sc.mu.Unlock()

	// Read length prefix (4 bytes)
	var lengthBuf [4]byte
	_, err := io.ReadFull(sc.conn, lengthBuf[:])
	if err != nil {
		return 0, err
	}
	length := binary.BigEndian.Uint32(lengthBuf[:])

	// Check packet size limit
	if length > maxRawPacketSize {
		return 0, ErrDecryptionFailed
	}

	// Read the ciphertext
	msgBuf := acquireBuffer(int(length))
	msgBuf.B = msgBuf.B[:length]
	defer releaseBuffer(msgBuf)
	_, err = io.ReadFull(sc.conn, msgBuf.B)
	if err != nil {
		return 0, err
	}

	// Minimum size: authentication tag
	if len(msgBuf.B) < noiseTagSize {
		return 0, ErrDecryptionFailed
	}

	// Decrypt in-place
	decrypted, err := sc.decryptor.Decrypt(msgBuf.B[:0], nil, msgBuf.B)
	if err != nil {
		return 0, ErrDecryptionFailed
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.closed {
		return 0, net.ErrClosed
	}

	// Copy decrypted data to the provided buffer
	n := copy(p, decrypted)
	if n < len(decrypted) {
		if sc.readBuffer == nil {
			sc.readBuffer = acquireBuffer(len(decrypted) - n)
		}
		sc.readBuffer.B = append(sc.readBuffer.B, decrypted[n:]...)
	}

	return n, nil
}

// Close closes the underlying connection and releases resources.
func (sc *SecureConnection) Close() error {
	sc.closeOnce.Do(func() {
		sc.mu.Lock()
		sc.closed = true
		if sc.readBuffer != nil {
			releaseBuffer(sc.readBuffer)
			sc.readBuffer = nil
		}
		sc.mu.Unlock()
		sc.closeErr = sc.conn.Close()
	})
	return sc.closeErr
}

// ClientHandshake performs the client-side (initiator) Noise XX handshake.
//
// Message flow:
//
//	Message 1 (client → server): e + ALPN payload (integrity-protected, not encrypted)
//	Message 2 (server → client): e, ee, s, es
//	Message 3 (client → server): s, se
func (h *Handshaker) ClientHandshake(ctx context.Context, conn io.ReadWriteCloser, alpn string) (*SecureConnection, error) {
	x25519Key := noise.DHKey{
		Private: h.credential.X25519PrivateKey(),
		Public:  h.credential.X25519PublicKey(),
	}

	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   newNoiseCipherSuite(),
		Pattern:       noise.HandshakeXX,
		Initiator:     true,
		StaticKeypair: x25519Key,
		Prologue:      []byte(noisePrologue),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: init: %w", ErrHandshakeFailed, err)
	}

	// Set deadline from context if the connection supports it
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		deadlineConn, supportsDeadline := conn.(interface{ SetDeadline(time.Time) error })
		if supportsDeadline {
			setErr := deadlineConn.SetDeadline(deadline)
			if setErr != nil {
				return nil, fmt.Errorf("%w: set deadline: %w", ErrHandshakeFailed, setErr)
			}
			defer deadlineConn.SetDeadline(time.Time{}) // Clear deadline after handshake
		}
	}

	// Message 1: → e + ALPN payload (integrity-protected via handshake hash)
	alpnPayload, err := encodeALPN(alpn)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHandshakeFailed, err)
	}
	msg1, _, _, err := hs.WriteMessage(nil, alpnPayload)
	if err != nil {
		return nil, fmt.Errorf("%w: write msg1: %w", ErrHandshakeFailed, err)
	}
	err = writeLengthPrefixed(conn, msg1)
	if err != nil {
		return nil, fmt.Errorf("%w: send msg1: %w", ErrHandshakeFailed, err)
	}

	// Message 2: ← e, ee, s, es
	msg2Raw, err := readLengthPrefixed(conn)
	if err != nil {
		return nil, fmt.Errorf("%w: recv msg2: %w", ErrHandshakeFailed, err)
	}
	_, _, _, err = hs.ReadMessage(nil, msg2Raw)
	if err != nil {
		return nil, fmt.Errorf("%w: read msg2: %w", ErrHandshakeFailed, err)
	}

	// Message 3: → s, se
	msg3, cs1, cs2, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: write msg3: %w", ErrHandshakeFailed, err)
	}
	err = writeLengthPrefixed(conn, msg3)
	if err != nil {
		return nil, fmt.Errorf("%w: send msg3: %w", ErrHandshakeFailed, err)
	}

	// cs1 = initiator→responder (client encrypt), cs2 = responder→initiator (client decrypt)
	localID := deriveConnectionID(x25519Key.Public)
	remoteID := deriveConnectionID(hs.PeerStatic())
	return h.createSecureConnection(conn, cs1, cs2, localID, remoteID), nil
}

// ServerHandshake performs the server-side (responder) Noise XX handshake.
//
// Message flow:
//
//	Message 1 (client → server): e + ALPN payload (integrity-protected, not encrypted)
//	Message 2 (server → client): e, ee, s, es
//	Message 3 (client → server): s, se
func (h *Handshaker) ServerHandshake(ctx context.Context, conn io.ReadWriteCloser, alpns []string) (*SecureConnection, error) {
	x25519Key := noise.DHKey{
		Private: h.credential.X25519PrivateKey(),
		Public:  h.credential.X25519PublicKey(),
	}

	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   newNoiseCipherSuite(),
		Pattern:       noise.HandshakeXX,
		Initiator:     false,
		StaticKeypair: x25519Key,
		Prologue:      []byte(noisePrologue),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: init: %w", ErrHandshakeFailed, err)
	}

	// Set deadline from context if the connection supports it
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		deadlineConn, supportsDeadline := conn.(interface{ SetDeadline(time.Time) error })
		if supportsDeadline {
			setErr := deadlineConn.SetDeadline(deadline)
			if setErr != nil {
				return nil, fmt.Errorf("%w: set deadline: %w", ErrHandshakeFailed, setErr)
			}
			defer deadlineConn.SetDeadline(time.Time{}) // Clear deadline after handshake
		}
	}

	// Message 1: ← e + ALPN payload
	msg1Raw, err := readLengthPrefixed(conn)
	if err != nil {
		return nil, fmt.Errorf("%w: recv msg1: %w", ErrHandshakeFailed, err)
	}
	alpnPayload, _, _, err := hs.ReadMessage(nil, msg1Raw)
	if err != nil {
		return nil, fmt.Errorf("%w: read msg1: %w", ErrHandshakeFailed, err)
	}

	// Validate ALPN before proceeding
	alpn, err := decodeALPN(alpnPayload)
	if err != nil || !slices.Contains(alpns, alpn) {
		if closeErr := conn.Close(); closeErr != nil {
			return nil, errors.Join(ErrHandshakeFailed, closeErr)
		}
		return nil, ErrHandshakeFailed
	}

	// Message 2: → e, ee, s, es
	msg2, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: write msg2: %w", ErrHandshakeFailed, err)
	}
	err = writeLengthPrefixed(conn, msg2)
	if err != nil {
		return nil, fmt.Errorf("%w: send msg2: %w", ErrHandshakeFailed, err)
	}

	// Message 3: ← s, se
	msg3Raw, err := readLengthPrefixed(conn)
	if err != nil {
		return nil, fmt.Errorf("%w: recv msg3: %w", ErrHandshakeFailed, err)
	}
	_, cs1, cs2, err := hs.ReadMessage(nil, msg3Raw)
	if err != nil {
		return nil, fmt.Errorf("%w: read msg3: %w", ErrHandshakeFailed, err)
	}

	// cs1 = initiator→responder (server decrypt), cs2 = responder→initiator (server encrypt)
	localID := deriveConnectionID(x25519Key.Public)
	remoteID := deriveConnectionID(hs.PeerStatic())
	return h.createSecureConnection(conn, cs2, cs1, localID, remoteID), nil
}

// createSecureConnection builds a SecureConnection from the completed handshake.
func (h *Handshaker) createSecureConnection(conn io.ReadWriteCloser, encryptor, decryptor *noise.CipherState, localID, remoteID string) *SecureConnection {
	readBuffer := acquireBuffer(1 << 12)
	readBuffer.B = readBuffer.B[:0]

	return &SecureConnection{
		conn:       conn,
		localID:    localID,
		remoteID:   remoteID,
		encryptor:  encryptor,
		decryptor:  decryptor,
		readBuffer: readBuffer,
	}
}

func deriveConnectionID(staticKey []byte) string {
	sum := sha256.Sum256(staticKey)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:16])
}

// encodeALPN encodes an ALPN string as [1B length][N bytes string].
func encodeALPN(alpn string) ([]byte, error) {
	if len(alpn) > 255 {
		return nil, fmt.Errorf("ALPN string too long: %d bytes (max 255)", len(alpn))
	}
	b := make([]byte, 1+len(alpn))
	b[0] = byte(len(alpn))
	copy(b[1:], alpn)
	return b, nil
}

// decodeALPN decodes an ALPN string from the [1B length][N bytes string] format.
func decodeALPN(payload []byte) (string, error) {
	if len(payload) < 1 {
		return "", ErrHandshakeFailed
	}
	alpnLen := int(payload[0])
	if len(payload) != 1+alpnLen {
		return "", ErrHandshakeFailed
	}
	return string(payload[1:]), nil
}

// writeFull writes all bytes to conn or returns an error.
func writeFull(conn io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := conn.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}

	return nil
}

// writeLengthPrefixed writes a 4-byte big-endian length prefix followed by the data.
func writeLengthPrefixed(conn io.Writer, data []byte) error {
	length := len(data)
	lengthBytes := []byte{
		byte(length >> 24),
		byte(length >> 16),
		byte(length >> 8),
		byte(length),
	}

	if err := writeFull(conn, lengthBytes); err != nil {
		return err
	}

	return writeFull(conn, data)
}

// readLengthPrefixed reads a 4-byte big-endian length prefix followed by the data.
func readLengthPrefixed(conn io.Reader) ([]byte, error) {
	lengthBytes := make([]byte, 4)
	if _, err := io.ReadFull(conn, lengthBytes); err != nil {
		return nil, err
	}

	length := int(lengthBytes[0])<<24 | int(lengthBytes[1])<<16 | int(lengthBytes[2])<<8 | int(lengthBytes[3])

	// Check packet size limit
	if length > maxRawPacketSize {
		return nil, ErrHandshakeFailed
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(conn, data); err != nil {
		return nil, err
	}

	return data, nil
}
