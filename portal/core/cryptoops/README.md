# Cryptographic Operations & End-to-End Encryption (E2EE)

This package implements a secure, authenticated end-to-end encryption protocol for Portal using modern cryptographic primitives and best practices.

## Table of Contents

- [Overview](#overview)
- [Cryptographic Primitives](#cryptographic-primitives)
- [Protocol Flow](#protocol-flow)
- [Key Derivation](#key-derivation)
- [Message Format](#message-format)
- [Security Properties](#security-properties)
- [Implementation Details](#implementation-details)
- [Error Handling](#error-handling)

## Overview

The E2EE protocol provides:
- **Mutual Authentication**: Both client and server verify each other's identities using Ed25519 signatures
- **Forward Secrecy**: Ephemeral X25519 key exchange ensures past sessions remain secure even if long-term keys are compromised
- **Confidentiality**: All application data is encrypted using ChaCha20-Poly1305 AEAD
- **Integrity**: AEAD authentication tags prevent tampering
- **Replay Protection**: Timestamps and random nonces prevent replay attacks

## Cryptographic Primitives

### 1. Ed25519 Digital Signatures
- **Purpose**: Long-term identity authentication
- **Key Size**: 32 bytes (256 bits)
- **Signature Size**: 64 bytes
- **Properties**: Deterministic, collision-resistant, provides non-repudiation

Each peer has a long-term Ed25519 keypair that identifies them:
```go
type Credential struct {
    privateKey ed25519.PrivateKey  // 64 bytes
    publicKey  ed25519.PublicKey   // 32 bytes
    id         string               // Base32-encoded HMAC-SHA256 of public key
}
```

#### Identity Derivation Algorithm

The ID field is deterministically derived from the Ed25519 public key using a secure hashing process:

```go
var _id_magic = []byte("RDVERB_PROTOCOL_VER_01_SHA256_ID")
var _base32_encoding = base32.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567").WithPadding(base32.NoPadding)

func DeriveID(publickey ed25519.PublicKey) string {
    h := hmac.New(sha256.New, _id_magic)
    h.Write(publickey)
    hash := h.Sum(nil)
    return _base32_encoding.EncodeToString(hash[:16])
}
```

**Algorithm Steps:**
1. **HMAC-SHA256**: Compute HMAC-SHA256(publicKey, "RDVERB_PROTOCOL_VER_01_SHA256_ID")
   - Input: 32-byte Ed25519 public key
   - HMAC Key: Protocol-specific magic string
   - Output: 32-byte hash
2. **Truncation**: Take first 128 bits (16 bytes) of hash
3. **Base32 Encoding**: Encode with custom alphabet (no padding)
   - Alphabet: `ABCDEFGHIJKLMNOPQRSTUVWXYZ234567`
   - Padding: None
   - Output: 26-character alphanumeric string

**Security Properties:**
- **Deterministic**: Same public key → same ID (enables caching and verification)
- **One-way**: Computationally infeasible to derive public key from ID
- **Collision-resistant**: 128-bit security provides ~10³⁸ unique IDs
- **Protocol-bound**: HMAC key prevents cross-protocol ID collision attacks
- **Compact**: 26 characters enable efficient URL encoding and database indexing


### 2. X25519 Key Exchange (Curve25519)
- **Purpose**: Ephemeral session key agreement
- **Key Size**: 32 bytes (256 bits)
- **Properties**: ECDH over Curve25519, provides forward secrecy

For each connection, both parties generate a fresh X25519 keypair:
```go
ephemeralPriv := make([]byte, 32)  // Scalar
ephemeralPub, _ := curve25519.X25519(ephemeralPriv, curve25519.Basepoint)
```

The shared secret is computed as:
```go
sharedSecret := curve25519.X25519(myPriv, theirPub)
```

### 3. ChaCha20-Poly1305 AEAD
- **Purpose**: Authenticated encryption of application data
- **Key Size**: 32 bytes (256 bits)
- **Nonce Size**: 12 bytes (96 bits)
- **Tag Size**: 16 bytes (128 bits)
- **Properties**: Fast, constant-time, provides confidentiality + authenticity

Each encrypted message includes:
- 12-byte random nonce (generated using CSPRNG)
- Ciphertext (same length as plaintext)
- 16-byte Poly1305 authentication tag

### 4. HKDF-SHA256 Key Derivation
- **Purpose**: Derive separate encryption keys from shared secret
- **Hash Function**: SHA-256
- **Properties**: Cryptographically strong key derivation, domain separation

Parameters:
- **IKM (Input Key Material)**: X25519 shared secret (32 bytes)
- **Salt**: Concatenation of both nonces (24 bytes)
- **Info**: Direction-specific context strings
  - Client → Server: `"RDSEC_KEY_CLIENT"`
  - Server → Client: `"RDSEC_KEY_SERVER"`
- **Output**: 32-byte symmetric keys

## Protocol Flow

### Phase 1: Client Initialization

1. **Generate Ephemeral Keypair**
   ```go
   clientEphemeralPriv, clientEphemeralPub := generateX25519KeyPair()
   ```

2. **Create ClientInitPayload**
   ```protobuf
   message ClientInitPayload {
     ProtocolVersion version = 1;        // PROTOCOL_VERSION_1
     bytes nonce = 2;                     // 12 random bytes
     int64 timestamp = 3;                 // Unix timestamp (seconds)
     Identity identity = 4;               // Client's Ed25519 identity
     string alpn = 5;                     // Application-Layer Protocol Negotiation
     bytes session_public_key = 6;        // clientEphemeralPub (32 bytes)
   }
   ```

3. **Sign and Send**
   ```go
   payloadBytes := proto.Marshal(clientInitPayload)
   signature := ed25519.Sign(clientPrivateKey, payloadBytes)

   signedPayload := &SignedPayload{
     Data:      payloadBytes,
     Signature: signature,
   }

   // Send length-prefixed message (4 bytes length + data)
   writeLengthPrefixed(conn, proto.Marshal(signedPayload))
   ```

### Phase 2: Server Validation and Response

1. **Receive and Validate Client Init**
   - Unmarshal SignedPayload
   - Verify protocol version is PROTOCOL_VERSION_1
   - Validate timestamp is within ±30 seconds
   - Verify ALPN matches expected value(s)
   - Validate identity structure (correct key sizes)
   - Verify Ed25519 signature using client's public key

   **Security Note**: If validation fails, server closes connection silently (no error response) to prevent information leakage.

2. **Generate Server Ephemeral Keypair**
   ```go
   serverEphemeralPriv, serverEphemeralPub := generateX25519KeyPair()
   ```

3. **Create and Send ServerInitPayload**
   - Similar structure to ClientInitPayload
   - Contains server's identity and ephemeral public key
   - Signed with server's Ed25519 private key

### Phase 3: Key Derivation

Both client and server independently derive the same shared secret but use it to create **different directional keys**:

```go
// Compute X25519 shared secret (identical for both parties)
sharedSecret := curve25519.X25519(myEphemeralPriv, theirEphemeralPub)

// Derive directional keys with HKDF
```

**Client's Key Derivation:**
```go
// Client encrypts with this key (Server decrypts)
salt := clientNonce || serverNonce
clientEncryptKey := HKDF-SHA256(sharedSecret, salt, "RDSEC_KEY_CLIENT")

// Client decrypts with this key (Server encrypts)
salt := serverNonce || clientNonce
clientDecryptKey := HKDF-SHA256(sharedSecret, salt, "RDSEC_KEY_SERVER")
```

**Server's Key Derivation:**
```go
// Server encrypts with this key (Client decrypts)
salt := serverNonce || clientNonce
serverEncryptKey := HKDF-SHA256(sharedSecret, salt, "RDSEC_KEY_SERVER")

// Server decrypts with this key (Client encrypts)
salt := clientNonce || serverNonce
serverDecryptKey := HKDF-SHA256(sharedSecret, salt, "RDSEC_KEY_CLIENT")
```

**Key Properties:**
- Different salts ensure different keys for each direction
- Info strings provide domain separation
- Both parties can communicate bidirectionally with different keys
- Nonce ordering in salt is critical for correctness

### Phase 4: Secure Communication

After handshake, all application data flows through `SecureConnection`:

```go
type SecureConnection struct {
    conn      io.ReadWriteCloser
    encryptor cipher.AEAD  // ChaCha20-Poly1305 with my encryption key
    decryptor cipher.AEAD  // ChaCha20-Poly1305 with my decryption key
    readBuffer *bytebufferpool.ByteBuffer
}
```

## Message Format

### Handshake Messages (Length-Prefixed)

```
+-------------------+-------------------+
| Length (4 bytes)  | Protobuf Payload  |
| Big Endian Uint32 | (variable length) |
+-------------------+-------------------+
```

### Encrypted Application Messages

```
+-------------------+-------------------+-------------------+-------------------+
| Length (4 bytes)  | Nonce (12 bytes)  | Ciphertext        | Tag (16 bytes)    |
| Big Endian Uint32 | Random            | (variable length) | Poly1305 MAC      |
+-------------------+-------------------+-------------------+-------------------+
```

**Length Field**: Total size of (nonce + ciphertext + tag)

**Fragmentation**: Messages larger than 32MB are automatically fragmented:
```go
const fragSize = maxRawPacketSize / 2  // 32MB
```

This prevents excessive memory allocation while maintaining compatibility with the relay server's 64MB packet limit.

### Encryption Process

```go
func (sc *SecureConnection) Write(p []byte) (int, error) {
    // 1. Generate random nonce
    nonce := randomBytes(12)

    // 2. Encrypt with AEAD
    ciphertext := encryptor.Seal(nil, nonce, plaintext, nil)
    // ciphertext = encrypted_data || tag

    // 3. Frame: length + nonce + ciphertext
    length := len(nonce) + len(ciphertext)
    frame := length (4 bytes) || nonce || ciphertext

    // 4. Write to connection
    conn.Write(frame)
}
```

### Decryption Process

```go
func (sc *SecureConnection) Read(p []byte) (int, error) {
    // 1. Read 4-byte length prefix
    lengthBytes := readFull(4)
    length := binary.BigEndian.Uint32(lengthBytes)

    // 2. Validate size limit
    if length > maxRawPacketSize {
        return error
    }

    // 3. Read encrypted message
    msgBytes := readFull(length)
    nonce := msgBytes[0:12]
    ciphertext := msgBytes[12:]

    // 4. Decrypt and authenticate
    plaintext, err := decryptor.Open(nil, nonce, ciphertext, nil)
    if err != nil {
        return ErrDecryptionFailed  // Authentication failed
    }

    // 5. Copy to output buffer
    copy(p, plaintext)
}
```

## Security Properties

### 1. Authentication
- **Mutual**: Both parties authenticate each other's long-term identities
- **Signature-based**: Ed25519 signatures over handshake payloads
- **Identity binding**: Public keys are cryptographically bound to identity IDs via HMAC-SHA256
  - HMAC key: `"RDVERB_PROTOCOL_VER_01_SHA256_ID"`
  - Output: First 128 bits of HMAC-SHA256(publicKey, magic)
  - Encoding: Base32 (custom alphabet, no padding)
  - Result: 26-character deterministic ID

### 2. Forward Secrecy
- **Ephemeral Keys**: Fresh X25519 keypair per connection
- **Perfect Forward Secrecy**: Compromise of long-term keys doesn't compromise past sessions
- **Session Isolation**: Each connection uses unique ephemeral keys

### 3. Confidentiality
- **Strong Cipher**: ChaCha20 stream cipher (256-bit security)
- **Unique Nonces**: Random nonces for each message prevent deterministic encryption
- **No IV Reuse**: CSPRNG-generated nonces ensure probabilistic encryption

### 4. Integrity & Authenticity
- **AEAD**: Poly1305 MAC provides 128-bit authentication
- **Tamper Detection**: Any modification causes decryption failure
- **No Decrypt-Then-Parse**: Authentication checked before processing

### 5. Replay Protection
- **Timestamp Validation**: Handshake messages must be within ±30 seconds
  ```go
  maxTimestampSkew = 30 * time.Second
  ```
- **Random Nonces**: Prevent message replay within session
- **No Sequence Numbers**: Stateless design, relies on AEAD and nonces

### 6. Resistance to Attacks

**Man-in-the-Middle (MitM)**:
- Attacker cannot forge Ed25519 signatures
- Cannot derive session keys without ephemeral private keys
- Signature verification prevents impersonation

**Replay Attacks**:
- Timestamp window limits handshake replay
- Unique ephemeral keys per session prevent session replay
- Random nonces prevent message replay

**Downgrade Attacks**:
- Protocol version explicitly checked
- Only PROTOCOL_VERSION_1 accepted
- Future versions can be added safely

**Denial of Service (DoS)**:
- Packet size limits prevent memory exhaustion
- Silent failure on invalid handshakes (no amplification)
- Constant-time operations where possible

**Side-Channel Attacks**:
- ChaCha20 is designed for constant-time operation
- Curve25519 uses constant-time implementation
- Sensitive keys wiped from memory after use
  ```go
  func wipeMemory(b []byte) {
      for i := range b {
          b[i] = 0
      }
  }
  ```

## Implementation Details

### Memory Management

The implementation uses careful memory management to minimize allocations and protect sensitive data:

```go
// Secure buffer pool for sensitive data
var _secureMemoryPool bytebufferpool.Pool

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

// Acquire buffer with auto-growing and alignment
func acquireBuffer(n int) *bytebufferpool.ByteBuffer {
	buffer := _secureMemoryPool.Get()
	if buffer.B == nil {
		buffer.B = make([]byte, 0)
	}
	bufferGrow(buffer, n)
	return buffer
}

// Release and wipe buffer
func releaseBuffer(buffer *bytebufferpool.ByteBuffer) {
    wipeMemory(buffer.B)  // Zero before returning to pool
    _secureMemoryPool.Put(buffer)
}
```

**Benefits:**
- Reduces GC pressure through pooling
- Prevents sensitive data from lingering in memory
- 16KB-aligned allocations for efficiency
- Constant-time memory wiping

### Random Number Generation

Cryptographically secure random numbers are critical:

```go
import "gosuda.org/portal/portal/internal/randpool"

// Generate random nonce
nonce := make([]byte, nonceSize)
randpool.CSPRNG_RAND(nonce)  // Uses crypto/rand internally
```

**Never use `math/rand`** for security-sensitive operations. All nonces, ephemeral keys, and IVs must come from a CSPRNG.

### Error Handling Strategy

```go
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
```

**Server Silent Failure**: When server validation fails during handshake, it closes the connection immediately without sending an error response. This prevents information leakage about why the handshake failed:

```go
if err := h.validateClientInit(...); err != nil {
    conn.Close()  // Silent close
    return nil, err
}
```

### ALPN (Application-Layer Protocol Negotiation)

ALPN allows protocol negotiation during handshake:

```go
// Client specifies desired protocol
clientInit.Alpn = "relay-v1"

// Server validates against allowed protocols
expectedAlpns := []string{"relay-v1", "relay-v2"}
if !slices.Contains(expectedAlpns, clientInit.Alpn) {
    return ErrHandshakeFailed
}

// Server echoes back the negotiated protocol
serverInit.Alpn = clientInit.Alpn
```

This enables protocol versioning and feature negotiation without breaking compatibility.

### Constants and Limits

```go
const (
    nonceSize        = 12               // ChaCha20Poly1305 standard nonce
    sessionKeySize   = 32               // 256-bit symmetric keys
    maxTimestampSkew = 30 * time.Second // Clock skew tolerance
    maxRawPacketSize = 1 << 26          // 64MB - matches relay server

    // Key derivation context strings
    clientKeyInfo = "RDSEC_KEY_CLIENT"
    serverKeyInfo = "RDSEC_KEY_SERVER"
)
```

### Read Buffer Management

`SecureConnection` maintains a read buffer to handle partial reads:

```go
type SecureConnection struct {
    readBuffer *bytebufferpool.ByteBuffer  // Stores leftover decrypted data
}

func (sc *SecureConnection) Read(p []byte) (int, error) {
    // First check if we have buffered data
    if len(sc.readBuffer.B) > 0 {
        n := copy(p, sc.readBuffer.B)
        // Shift remaining data to front
        copy(sc.readBuffer.B[:len(sc.readBuffer.B)-n], sc.readBuffer.B[n:])
        sc.readBuffer.B = sc.readBuffer.B[:len(sc.readBuffer.B)-n]
        return n, nil
    }

    // Otherwise, decrypt new packet...
}
```

This ensures correct behavior when the output buffer is smaller than the decrypted message.

## Best Practices

### DO ✓

1. **Always validate protocol version** before processing handshake messages
2. **Check timestamp** within reasonable window (±30s)
3. **Verify signatures** before trusting identity claims
4. **Use unique nonces** for each encrypted message
5. **Wipe sensitive data** from memory after use
6. **Limit packet sizes** to prevent resource exhaustion
7. **Use constant-time operations** where possible
8. **Handle errors securely** (no information leakage)

### DON'T ✗

1. **Never reuse nonces** with the same key
2. **Never skip signature verification**
3. **Never trust timestamps** without validation
4. **Never send error details** in handshake failures
5. **Never use `math/rand`** for security operations
6. **Never ignore return values** from crypto functions
7. **Never log sensitive data** (keys, plaintexts)
8. **Never implement custom crypto** without expert review

## Testing Considerations

When testing this implementation:

1. **Handshake Tests**
   - Valid handshake flows (client and server)
   - Invalid signatures
   - Timestamp skew scenarios
   - Protocol version mismatches
   - Invalid ALPN
   - Malformed messages

2. **Encryption Tests**
   - Round-trip encryption/decryption
   - Large messages (fragmentation)
   - Concurrent reads/writes
   - Buffer boundary conditions

3. **Security Tests**
   - Replay attack resistance
   - Tampering detection
   - Key isolation between sessions
   - Memory wiping verification

4. **Integration Tests**
   - End-to-end communication
   - Error propagation
   - Connection lifecycle
   - Performance benchmarks

## References

- **X25519**: [RFC 7748](https://tools.ietf.org/html/rfc7748)
- **Ed25519**: [RFC 8032](https://tools.ietf.org/html/rfc8032)
- **ChaCha20-Poly1305**: [RFC 8439](https://tools.ietf.org/html/rfc8439)
- **HKDF**: [RFC 5869](https://tools.ietf.org/html/rfc5869)
- **ALPN**: [RFC 7301](https://tools.ietf.org/html/rfc7301)

## Changelog

### Version 1.0 (Current)
- Initial implementation
- X25519 + ChaCha20-Poly1305 AEAD
- Ed25519 identity signatures
- HKDF-SHA256 key derivation
- Timestamp-based replay protection
- ALPN support
- Automatic message fragmentation
