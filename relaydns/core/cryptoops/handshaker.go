package cryptoops

import (
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
	"google.golang.org/protobuf/proto"

	"github.com/gosuda/relaydns/relaydns/core/proto/rdsec"
)

var (
	ErrHandshakeFailed  = errors.New("handshake failed")
	ErrInvalidSignature = errors.New("invalid signature")
	ErrInvalidTimestamp = errors.New("invalid timestamp")
	ErrInvalidProtocol  = errors.New("invalid protocol version")
	ErrInvalidIdentity  = errors.New("invalid identity")
	ErrSessionKeyDerive = errors.New("failed to derive session key")
	ErrEncryptionFailed = errors.New("encryption failed")
	ErrDecryptionFailed = errors.New("decryption failed")
	ErrInvalidNonce     = errors.New("invalid nonce")
)

const (
	nonceSize        = 12 // ChaCha20Poly1305 nonce size
	sessionKeySize   = 32 // X25519 shared secret size
	maxTimestampSkew = 30 * time.Second
	maxRawPacketSize = 1 << 26 // 64MB - same as relay server

	// HKDF info strings for key derivation
	clientKeyInfo = "RDSEC_KEY_CLIENT"
	serverKeyInfo = "RDSEC_KEY_SERVER"
)

// Handshaker handles the X25519-ChaCha20Poly1305 based handshake protocol
type Handshaker struct {
	credential *Credential
}

// NewHandshaker creates a new Handshaker with the given credential
func NewHandshaker(credential *Credential) *Handshaker {
	return &Handshaker{
		credential: credential,
	}
}

// SecureConnection represents a secured connection with encryption capabilities
type SecureConnection struct {
	conn         io.ReadWriteCloser
	encryptor    cipher.AEAD
	decryptor    cipher.AEAD
	encryptNonce []byte
	decryptNonce []byte
}

// Write encrypts and writes data to the underlying connection
func (sc *SecureConnection) Write(p []byte) (int, error) {
	// Increment nonce for each message
	incrementNonce(sc.encryptNonce)

	// Encrypt the data
	encrypted := sc.encryptor.Seal(nil, sc.encryptNonce, p, nil)

	// Create EncryptedData message
	encryptedData := &rdsec.EncryptedData{
		Nonce:   make([]byte, len(sc.encryptNonce)),
		Payload: encrypted,
	}
	copy(encryptedData.Nonce, sc.encryptNonce)

	// Serialize and send
	data, err := proto.Marshal(encryptedData)
	if err != nil {
		return 0, ErrEncryptionFailed
	}

	// Check packet size limit
	if len(data) > maxRawPacketSize {
		return 0, ErrEncryptionFailed
	}

	// Write length-prefixed message
	if err := writeLengthPrefixed(sc.conn, data); err != nil {
		return 0, err
	}

	return len(p), nil
}

// Read reads and decrypts data from the underlying connection
func (sc *SecureConnection) Read(p []byte) (int, error) {
	// Read the encrypted data message
	encryptedData := &rdsec.EncryptedData{}

	// Read length prefix first (4 bytes)
	lengthBuf := make([]byte, 4)
	_, err := io.ReadFull(sc.conn, lengthBuf)
	if err != nil {
		return 0, err
	}

	// Calculate message length
	length := int(lengthBuf[0])<<24 | int(lengthBuf[1])<<16 | int(lengthBuf[2])<<8 | int(lengthBuf[3])

	// Check packet size limit
	if length > maxRawPacketSize {
		return 0, ErrDecryptionFailed
	}

	// Read the message
	msgBuf := make([]byte, length)
	_, err = io.ReadFull(sc.conn, msgBuf)
	if err != nil {
		return 0, err
	}

	// Unmarshal the message
	err = proto.Unmarshal(msgBuf, encryptedData)
	if err != nil {
		return 0, ErrDecryptionFailed
	}

	// Validate nonce
	if len(encryptedData.Nonce) != nonceSize {
		return 0, ErrInvalidNonce
	}

	// Decrypt the data
	decrypted, err := sc.decryptor.Open(nil, encryptedData.Nonce, encryptedData.Payload, nil)
	if err != nil {
		return 0, ErrDecryptionFailed
	}

	// Copy decrypted data to the provided buffer
	copy(p, decrypted)
	return len(decrypted), nil
}

// Close closes the underlying connection
func (sc *SecureConnection) Close() error {
	return sc.conn.Close()
}

// ClientHandshake performs the client-side of the handshake
func (h *Handshaker) ClientHandshake(conn io.ReadWriteCloser, alpn string) (*SecureConnection, error) {
	// Generate ephemeral key pair for this session
	ephemeralPriv, ephemeralPub, err := generateX25519KeyPair()
	if err != nil {
		return nil, ErrHandshakeFailed
	}

	// Create client init message
	timestamp := time.Now().Unix()
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrHandshakeFailed
	}

	clientInitPayload := &rdsec.ClientInitPayload{
		Version:   rdsec.ProtocolVersion_PROTOCOL_VERSION_1,
		Nonce:     nonce,
		Timestamp: timestamp,
		Identity: &rdsec.Identity{
			Id:        h.credential.ID(),
			PublicKey: h.credential.PublicKey(),
		},
		Alpn:             alpn,
		SessionPublicKey: ephemeralPub,
	}

	// Serialize and sign the payload
	payloadBytes, err := proto.Marshal(clientInitPayload)
	if err != nil {
		return nil, ErrHandshakeFailed
	}

	signature := h.credential.Sign(payloadBytes)

	clientInit := &rdsec.SignedPayload{
		Data:      payloadBytes,
		Signature: signature,
	}

	// Send client init message
	clientInitBytes, err := proto.Marshal(clientInit)
	if err != nil {
		return nil, ErrHandshakeFailed
	}

	// Write length-prefixed message
	if err := writeLengthPrefixed(conn, clientInitBytes); err != nil {
		return nil, ErrHandshakeFailed
	}

	// Read server init response
	serverInitBytes, err := readLengthPrefixed(conn)
	if err != nil {
		return nil, ErrHandshakeFailed
	}

	serverInitSigned := &rdsec.SignedPayload{}
	if err := proto.Unmarshal(serverInitBytes, serverInitSigned); err != nil {
		return nil, ErrHandshakeFailed
	}

	// Unmarshal the server init payload
	serverInitPayload := &rdsec.ServerInitPayload{}
	if err := proto.Unmarshal(serverInitSigned.GetData(), serverInitPayload); err != nil {
		return nil, ErrHandshakeFailed
	}

	// Validate server init
	if err := h.validateServerInit(serverInitSigned, serverInitPayload); err != nil {
		return nil, err
	}

	// Derive session keys
	clientEncryptKey, clientDecryptKey, err := h.deriveClientSessionKeys(
		ephemeralPriv, ephemeralPub, serverInitPayload.GetSessionPublicKey(),
		clientInitPayload.GetNonce(), serverInitPayload.GetNonce(),
	)
	if err != nil {
		return nil, err
	}

	// Create secure connection
	return h.createSecureConnection(conn, clientEncryptKey, clientDecryptKey, nonce, serverInitPayload.GetNonce())
}

// ServerHandshake performs the server-side of the handshake
func (h *Handshaker) ServerHandshake(conn io.ReadWriteCloser, alpn string) (*SecureConnection, error) {
	// Read client init message
	clientInitBytes, err := readLengthPrefixed(conn)
	if err != nil {
		return nil, ErrHandshakeFailed
	}

	clientInitSigned := &rdsec.SignedPayload{}
	if err := proto.Unmarshal(clientInitBytes, clientInitSigned); err != nil {
		return nil, ErrHandshakeFailed
	}

	// Unmarshal the client init payload
	clientInitPayload := &rdsec.ClientInitPayload{}
	if err := proto.Unmarshal(clientInitSigned.GetData(), clientInitPayload); err != nil {
		return nil, ErrHandshakeFailed
	}

	// Validate client init
	if err := h.validateClientInit(clientInitSigned, clientInitPayload, alpn); err != nil {
		// Silent failure: close connection and return error without sending response
		conn.Close()
		return nil, err
	}

	// Generate ephemeral key pair for this session
	ephemeralPriv, ephemeralPub, err := generateX25519KeyPair()
	if err != nil {
		return nil, ErrHandshakeFailed
	}

	// Create server init message
	timestamp := time.Now().Unix()
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrHandshakeFailed
	}

	serverInitPayload := &rdsec.ServerInitPayload{
		Version:   rdsec.ProtocolVersion_PROTOCOL_VERSION_1,
		Nonce:     nonce,
		Timestamp: timestamp,
		Identity: &rdsec.Identity{
			Id:        h.credential.ID(),
			PublicKey: h.credential.PublicKey(),
		},
		Alpn:             alpn,
		SessionPublicKey: ephemeralPub,
	}

	// Serialize and sign the payload
	payloadBytes, err := proto.Marshal(serverInitPayload)
	if err != nil {
		return nil, ErrHandshakeFailed
	}

	signature := h.credential.Sign(payloadBytes)

	serverInit := &rdsec.SignedPayload{
		Data:      payloadBytes,
		Signature: signature,
	}

	// Derive session keys
	serverEncryptKey, serverDecryptKey, err := h.deriveServerSessionKeys(
		ephemeralPriv, ephemeralPub, clientInitPayload.GetSessionPublicKey(),
		clientInitPayload.GetNonce(), nonce,
	)
	if err != nil {
		return nil, err
	}

	// Send server init message
	serverInitBytes, err := proto.Marshal(serverInit)
	if err != nil {
		return nil, ErrHandshakeFailed
	}

	// Write length-prefixed message
	if err := writeLengthPrefixed(conn, serverInitBytes); err != nil {
		return nil, ErrHandshakeFailed
	}

	// Create secure connection
	return h.createSecureConnection(conn, serverEncryptKey, serverDecryptKey, nonce, clientInitPayload.GetNonce())
}

// validateClientInit validates the client init message
func (h *Handshaker) validateClientInit(clientInitSigned *rdsec.SignedPayload, clientInitPayload *rdsec.ClientInitPayload, expectedAlpn string) error {
	if clientInitSigned == nil || clientInitPayload == nil {
		return ErrInvalidProtocol
	}

	// Check protocol version
	if clientInitPayload.GetVersion() != rdsec.ProtocolVersion_PROTOCOL_VERSION_1 {
		return ErrInvalidProtocol
	}

	// Check timestamp
	if err := validateTimestamp(clientInitPayload.GetTimestamp()); err != nil {
		return err
	}

	// Check ALPN
	if clientInitPayload.GetAlpn() != expectedAlpn {
		return ErrHandshakeFailed
	}

	// Validate identity
	if !ValidateIdentity(clientInitPayload.GetIdentity()) {
		return ErrInvalidIdentity
	}

	// Verify signature
	if !ed25519.Verify(clientInitPayload.GetIdentity().GetPublicKey(), clientInitSigned.GetData(), clientInitSigned.GetSignature()) {
		return ErrInvalidSignature
	}

	return nil
}

// validateServerInit validates the server init message
func (h *Handshaker) validateServerInit(serverInitSigned *rdsec.SignedPayload, serverInitPayload *rdsec.ServerInitPayload) error {
	if serverInitSigned == nil || serverInitPayload == nil {
		return ErrInvalidProtocol
	}

	// Check protocol version
	if serverInitPayload.GetVersion() != rdsec.ProtocolVersion_PROTOCOL_VERSION_1 {
		return ErrInvalidProtocol
	}

	// Check timestamp
	if err := validateTimestamp(serverInitPayload.GetTimestamp()); err != nil {
		return err
	}

	// Validate identity
	if !ValidateIdentity(serverInitPayload.GetIdentity()) {
		return ErrInvalidIdentity
	}

	// Verify signature
	if !ed25519.Verify(serverInitPayload.GetIdentity().GetPublicKey(), serverInitSigned.GetData(), serverInitSigned.GetSignature()) {
		return ErrInvalidSignature
	}

	return nil
}

// deriveClientSessionKeys derives encryption and decryption keys for the client
func (h *Handshaker) deriveClientSessionKeys(clientPriv, clientPub, serverPub, clientNonce, serverNonce []byte) ([]byte, []byte, error) {
	// Compute shared secret
	sharedSecret, err := curve25519.X25519(clientPriv, serverPub)
	if err != nil {
		return nil, nil, ErrSessionKeyDerive
	}

	// Derive keys using HKDF-like construction
	// Both client and server use the same derivation for the same direction
	// Client encrypts, server decrypts
	salt := append(clientNonce, serverNonce...)
	encryptKey := deriveKey(sharedSecret, salt, []byte(clientKeyInfo))
	// Server encrypts, client decrypts
	salt = append(serverNonce, clientNonce...)
	decryptKey := deriveKey(sharedSecret, salt, []byte(serverKeyInfo))

	return encryptKey, decryptKey, nil
}

// deriveServerSessionKeys derives encryption and decryption keys for the server
func (h *Handshaker) deriveServerSessionKeys(serverPriv, serverPub, clientPub, clientNonce, serverNonce []byte) ([]byte, []byte, error) {
	// Compute shared secret (should be same as client's)
	sharedSecret, err := curve25519.X25519(serverPriv, clientPub)
	if err != nil {
		return nil, nil, ErrSessionKeyDerive
	}

	// Derive keys using HKDF-like construction
	// Both client and server use the same derivation for the same direction
	// Server encrypts, client decrypts
	salt := append(serverNonce, clientNonce...)
	encryptKey := deriveKey(sharedSecret, salt, []byte(serverKeyInfo))
	// Client encrypts, server decrypts
	salt = append(clientNonce, serverNonce...)
	decryptKey := deriveKey(sharedSecret, salt, []byte(clientKeyInfo))

	return encryptKey, decryptKey, nil
}

// createSecureConnection creates a new SecureConnection with the given keys and nonces
func (h *Handshaker) createSecureConnection(conn io.ReadWriteCloser, encryptKey, decryptKey, encryptNonce, decryptNonce []byte) (*SecureConnection, error) {
	// Create AEAD instances
	encryptor, err := chacha20poly1305.New(encryptKey)
	if err != nil {
		return nil, ErrEncryptionFailed
	}

	decryptor, err := chacha20poly1305.New(decryptKey)
	if err != nil {
		return nil, ErrEncryptionFailed
	}

	// Copy nonces to avoid modifying the originals
	encNonce := make([]byte, nonceSize)
	decNonce := make([]byte, nonceSize)
	copy(encNonce, encryptNonce)
	copy(decNonce, decryptNonce)

	return &SecureConnection{
		conn:         conn,
		encryptor:    encryptor,
		decryptor:    decryptor,
		encryptNonce: encNonce,
		decryptNonce: decNonce,
	}, nil
}

// Helper functions

// generateX25519KeyPair generates a new X25519 key pair
func generateX25519KeyPair() ([]byte, []byte, error) {
	priv := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(priv); err != nil {
		return nil, nil, err
	}

	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, nil, err
	}

	return priv, pub, nil
}

// deriveKey derives a key from the shared secret using HKDF-SHA256
func deriveKey(sharedSecret, salt, info []byte) []byte {
	hkdf := hkdf.New(sha256.New, sharedSecret, salt, info)
	key := make([]byte, sessionKeySize)
	if _, err := hkdf.Read(key); err != nil {
		// HKDF should never fail with valid inputs, treat as critical error
		panic(fmt.Sprintf("HKDF key derivation failed: %v", err))
	}
	return key
}

// validateTimestamp validates that the timestamp is within acceptable range
func validateTimestamp(timestamp int64) error {
	now := time.Now().Unix()
	diff := now - timestamp

	if diff < -int64(maxTimestampSkew.Seconds()) || diff > int64(maxTimestampSkew.Seconds()) {
		return ErrInvalidTimestamp
	}

	return nil
}

// incrementNonce increments the nonce for the next message
func incrementNonce(nonce []byte) {
	// Simple increment - in production, you might want a more sophisticated approach
	for i := len(nonce) - 1; i >= 0; i-- {
		nonce[i]++
		if nonce[i] != 0 {
			break
		}
	}
}

// writeLengthPrefixed writes a length-prefixed message to the connection
func writeLengthPrefixed(conn io.Writer, data []byte) error {
	length := len(data)
	lengthBytes := []byte{
		byte(length >> 24),
		byte(length >> 16),
		byte(length >> 8),
		byte(length),
	}

	if _, err := conn.Write(lengthBytes); err != nil {
		return err
	}

	_, err := conn.Write(data)
	return err
}

// readLengthPrefixed reads a length-prefixed message from the connection
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
