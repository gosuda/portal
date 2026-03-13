package portal

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"

	"golang.org/x/crypto/hkdf"

	"github.com/gosuda/portal/v2/utils"
)

// QUIC v1 constants (RFC 9001).
var quicV1InitialSalt = []byte{
	0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3,
	0x4d, 0x17, 0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad,
	0xcc, 0xbb, 0x7f, 0x0a,
}

var errNotQUICInitial = errors.New("not a quic initial packet")
var errSNINotFound = errors.New("sni not found in quic initial")

// parseQUICInitialSNI extracts the TLS SNI from a QUIC Initial packet.
// It decrypts the Initial packet header and payload using keys derived from
// the Destination Connection ID per RFC 9001 Section 5.2, then parses the
// CRYPTO frame to find the TLS ClientHello SNI extension.
func parseQUICInitialSNI(packet []byte) (string, error) {
	if len(packet) < 5 {
		return "", errNotQUICInitial
	}

	// Long header: first bit is 1, second bit is 1 (fixed), bits 4-5 are packet type.
	firstByte := packet[0]
	if firstByte&0x80 == 0 {
		return "", errNotQUICInitial // short header
	}

	// Packet type: bits 4-5 of first byte. Initial = 0.
	packetType := (firstByte & 0x30) >> 4
	if packetType != 0 {
		return "", errNotQUICInitial
	}

	// Version (4 bytes).
	version := binary.BigEndian.Uint32(packet[1:5])
	if version == 0 {
		return "", errNotQUICInitial // version negotiation
	}

	offset := 5

	// Destination Connection ID length + DCID.
	if offset >= len(packet) {
		return "", errNotQUICInitial
	}
	dcidLen := int(packet[offset])
	offset++
	if offset+dcidLen > len(packet) {
		return "", errNotQUICInitial
	}
	dcid := packet[offset : offset+dcidLen]
	offset += dcidLen

	// Source Connection ID length + SCID.
	if offset >= len(packet) {
		return "", errNotQUICInitial
	}
	scidLen := int(packet[offset])
	offset++
	offset += scidLen
	if offset > len(packet) {
		return "", errNotQUICInitial
	}

	// Token length (varint) + token.
	tokenLen, n := readVarint(packet[offset:])
	if n <= 0 {
		return "", errNotQUICInitial
	}
	offset += n + int(tokenLen)
	if offset > len(packet) {
		return "", errNotQUICInitial
	}

	// Payload length (varint).
	payloadLen, n := readVarint(packet[offset:])
	if n <= 0 {
		return "", errNotQUICInitial
	}
	offset += n
	_ = payloadLen

	// The rest from offset is: packet number (1-4 bytes, encrypted) + encrypted payload.
	// We need to decrypt the header first to determine packet number length.
	clientSecret, err := deriveInitialClientSecret(dcid, version)
	if err != nil {
		return "", fmt.Errorf("derive initial secret: %w", err)
	}

	hp, err := deriveHPKey(clientSecret)
	if err != nil {
		return "", fmt.Errorf("derive hp key: %w", err)
	}

	key, err := deriveKey(clientSecret)
	if err != nil {
		return "", fmt.Errorf("derive key: %w", err)
	}

	iv, err := deriveIV(clientSecret)
	if err != nil {
		return "", fmt.Errorf("derive iv: %w", err)
	}

	// Header protection: sample 16 bytes starting 4 bytes after packet number offset.
	pnOffset := offset
	sampleOffset := pnOffset + 4
	if sampleOffset+16 > len(packet) {
		return "", errNotQUICInitial
	}
	sample := packet[sampleOffset : sampleOffset+16]

	// Create AES-ECB cipher for HP mask.
	block, err := aes.NewCipher(hp)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	mask := make([]byte, aes.BlockSize)
	block.Encrypt(mask, sample)

	// Unmask first byte.
	unmaskedFirst := packet[0] ^ (mask[0] & 0x0f) // long header: lower 4 bits
	pnLength := int(unmaskedFirst&0x03) + 1

	// Unmask packet number.
	pnBytes := make([]byte, pnLength)
	for i := range pnLength {
		pnBytes[i] = packet[pnOffset+i] ^ mask[1+i]
	}

	var pn uint32
	for _, b := range pnBytes {
		pn = (pn << 8) | uint32(b)
	}

	// Build nonce for AEAD.
	nonce := make([]byte, len(iv))
	copy(nonce, iv)
	for i := range len(nonce) {
		if i >= len(nonce)-4 {
			nonce[i] ^= byte(pn >> (8 * (len(nonce) - 1 - i)))
		}
	}

	// Decrypt payload.
	payloadOffset := pnOffset + pnLength
	if payloadOffset >= len(packet) {
		return "", errNotQUICInitial
	}

	// AAD = entire header with unmasked first byte and unmasked PN.
	aad := make([]byte, payloadOffset)
	copy(aad, packet[:payloadOffset])
	aad[0] = unmaskedFirst
	copy(aad[pnOffset:], pnBytes)

	aead, err := cipher.NewGCM(block)
	if err != nil {
		// Use the key for AEAD, not HP block.
		aeadBlock, err2 := aes.NewCipher(key)
		if err2 != nil {
			return "", fmt.Errorf("aead cipher: %w", err2)
		}
		aead, err = cipher.NewGCM(aeadBlock)
		if err != nil {
			return "", fmt.Errorf("gcm: %w", err)
		}
	} else {
		// We used the HP block for AEAD by mistake. Redo with key.
		aeadBlock, err2 := aes.NewCipher(key)
		if err2 != nil {
			return "", fmt.Errorf("aead cipher: %w", err2)
		}
		aead, err = cipher.NewGCM(aeadBlock)
		if err != nil {
			return "", fmt.Errorf("gcm: %w", err)
		}
	}

	ciphertext := packet[payloadOffset:]
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return "", fmt.Errorf("decrypt initial payload: %w", err)
	}

	// Parse CRYPTO frames to find ClientHello.
	return extractSNIFromCryptoFrames(plaintext)
}

// extractSNIFromCryptoFrames parses QUIC frames looking for CRYPTO frames
// containing a TLS ClientHello, and extracts the SNI server_name extension.
func extractSNIFromCryptoFrames(frames []byte) (string, error) {
	offset := 0
	for offset < len(frames) {
		frameType := frames[offset]
		offset++

		switch {
		case frameType == 0x00:
			// PADDING frame — skip.
			continue
		case frameType == 0x01:
			// PING frame — skip.
			continue
		case frameType == 0x06:
			// CRYPTO frame.
			// Offset field (varint).
			_, n := readVarint(frames[offset:])
			if n <= 0 {
				return "", errSNINotFound
			}
			offset += n

			// Length field (varint).
			dataLen, n := readVarint(frames[offset:])
			if n <= 0 {
				return "", errSNINotFound
			}
			offset += n

			if offset+int(dataLen) > len(frames) {
				return "", errSNINotFound
			}
			cryptoData := frames[offset : offset+int(dataLen)]
			offset += int(dataLen)

			sni, err := parseTLSClientHelloSNI(cryptoData)
			if err == nil {
				return sni, nil
			}
		default:
			// Unknown frame — can't continue parsing reliably.
			return "", errSNINotFound
		}
	}
	return "", errSNINotFound
}

// parseTLSClientHelloSNI parses a raw TLS ClientHello message and extracts SNI.
func parseTLSClientHelloSNI(data []byte) (string, error) {
	// TLS handshake: type(1) + length(3) + ...
	if len(data) < 4 {
		return "", errSNINotFound
	}
	if data[0] != 0x01 { // ClientHello
		return "", errSNINotFound
	}
	msgLen := int(data[1])<<16 | int(data[2])<<8 | int(data[3])
	if len(data) < 4+msgLen {
		return "", errSNINotFound
	}
	body := data[4 : 4+msgLen]

	// ClientHello: version(2) + random(32) + session_id_len(1) + session_id + ...
	if len(body) < 34 {
		return "", errSNINotFound
	}
	offset := 2 + 32 // skip version + random

	// Session ID.
	if offset >= len(body) {
		return "", errSNINotFound
	}
	sessionIDLen := int(body[offset])
	offset += 1 + sessionIDLen

	// Cipher suites.
	if offset+2 > len(body) {
		return "", errSNINotFound
	}
	cipherSuitesLen := int(body[offset])<<8 | int(body[offset+1])
	offset += 2 + cipherSuitesLen

	// Compression methods.
	if offset >= len(body) {
		return "", errSNINotFound
	}
	compMethodsLen := int(body[offset])
	offset += 1 + compMethodsLen

	// Extensions.
	if offset+2 > len(body) {
		return "", errSNINotFound
	}
	extensionsLen := int(body[offset])<<8 | int(body[offset+1])
	offset += 2

	extEnd := offset + extensionsLen
	if extEnd > len(body) {
		extEnd = len(body)
	}

	for offset+4 <= extEnd {
		extType := int(body[offset])<<8 | int(body[offset+1])
		extLen := int(body[offset+2])<<8 | int(body[offset+3])
		offset += 4

		if extType == 0x0000 { // server_name
			return parseSNIExtension(body[offset : offset+extLen])
		}
		offset += extLen
	}

	return "", errSNINotFound
}

func parseSNIExtension(data []byte) (string, error) {
	if len(data) < 2 {
		return "", errSNINotFound
	}
	// Server name list length.
	listLen := int(data[0])<<8 | int(data[1])
	offset := 2
	end := offset + listLen
	if end > len(data) {
		end = len(data)
	}

	for offset+3 <= end {
		nameType := data[offset]
		nameLen := int(data[offset+1])<<8 | int(data[offset+2])
		offset += 3
		if nameType == 0x00 { // host_name
			if offset+nameLen > end {
				return "", errSNINotFound
			}
			return string(data[offset : offset+nameLen]), nil
		}
		offset += nameLen
	}
	return "", errSNINotFound
}

// QUIC Initial secret derivation (RFC 9001 Section 5.2).
func deriveInitialClientSecret(dcid []byte, version uint32) ([]byte, error) {
	salt := quicV1InitialSalt

	initialSecret := hkdf.Extract(crypto.SHA256.New, dcid, salt)

	clientInitialSecret := make([]byte, 32)
	r := hkdf.Expand(crypto.SHA256.New, initialSecret, hkdfLabel([]byte("client in"), 32))
	if _, err := r.Read(clientInitialSecret); err != nil {
		return nil, err
	}
	return clientInitialSecret, nil
}

func deriveHPKey(secret []byte) ([]byte, error) {
	hp := make([]byte, 16)
	r := hkdf.Expand(crypto.SHA256.New, secret, hkdfLabel([]byte("quic hp"), 16))
	if _, err := r.Read(hp); err != nil {
		return nil, err
	}
	return hp, nil
}

func deriveKey(secret []byte) ([]byte, error) {
	key := make([]byte, 16)
	r := hkdf.Expand(crypto.SHA256.New, secret, hkdfLabel([]byte("quic key"), 16))
	if _, err := r.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func deriveIV(secret []byte) ([]byte, error) {
	iv := make([]byte, 12)
	r := hkdf.Expand(crypto.SHA256.New, secret, hkdfLabel([]byte("quic iv"), 12))
	if _, err := r.Read(iv); err != nil {
		return nil, err
	}
	return iv, nil
}

// hkdfLabel builds a TLS 1.3 HkdfLabel structure for HKDF-Expand-Label.
func hkdfLabel(label []byte, length int) []byte {
	fullLabel := append([]byte("tls13 "), label...)
	out := make([]byte, 2+1+len(fullLabel)+1)
	out[0] = byte(length >> 8)
	out[1] = byte(length)
	out[2] = byte(len(fullLabel))
	copy(out[3:], fullLabel)
	out[3+len(fullLabel)] = 0 // empty context
	return out
}

// readVarint reads a QUIC variable-length integer (RFC 9000 Section 16).
func readVarint(data []byte) (uint64, int) {
	if len(data) == 0 {
		return 0, -1
	}
	prefix := data[0] >> 6
	length := 1 << prefix

	if len(data) < length {
		return 0, -1
	}

	val := uint64(data[0] & 0x3f)
	for i := 1; i < length; i++ {
		val = (val << 8) | uint64(data[i])
	}
	return val, length
}

// quicSNIRouter listens on a raw UDP socket and routes QUIC connections
// based on SNI extracted from Initial packets.
type quicSNIRouter struct {
	conn       net.PacketConn
	server     *Server
	connTable  map[string]string // "src_ip:port" → leaseID
	mu         sync.RWMutex
	done       chan struct{}
	closeOnce  sync.Once
}

func newQUICSNIRouter(conn net.PacketConn, server *Server) *quicSNIRouter {
	return &quicSNIRouter{
		conn:      conn,
		server:    server,
		connTable: make(map[string]string),
		done:      make(chan struct{}),
	}
}

func (r *quicSNIRouter) run() error {
	buf := make([]byte, 65535)
	for {
		n, addr, err := r.conn.ReadFrom(buf)
		if err != nil {
			select {
			case <-r.done:
				return nil
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		packet := make([]byte, n)
		copy(packet, buf[:n])
		go r.handlePacket(packet, addr)
	}
}

func (r *quicSNIRouter) handlePacket(packet []byte, srcAddr net.Addr) {
	key := srcAddr.String()

	// Check if we already have a mapping for this source.
	r.mu.RLock()
	leaseID, found := r.connTable[key]
	r.mu.RUnlock()

	if !found {
		// Try to parse SNI from Initial packet.
		sni, err := parseQUICInitialSNI(packet)
		if err != nil || sni == "" {
			return // drop non-Initial or unparseable packets from unknown sources
		}

		serverName := utils.NormalizeHostname(sni)
		record, ok := r.server.registry.Lookup(serverName)
		if !ok || record == nil {
			return // no route
		}
		leaseID = record.ID

		r.mu.Lock()
		r.connTable[key] = leaseID
		r.mu.Unlock()
	}

	// Forward packet to the tunnel via QUIC DATAGRAM.
	record, ok := r.server.registry.Get(leaseID)

	if !ok || record == nil || record.QUICBroker == nil || !record.QUICBroker.HasConnection() {
		return
	}

	udpAddr, ok := srcAddr.(*net.UDPAddr)
	if !ok {
		return
	}

	flowID := record.QUICBroker.AllocateFlow(udpAddr)
	_ = record.QUICBroker.SendDatagram(flowID, packet)
}

// writeBackLoop reads datagrams from each QUIC broker and writes raw UDP back
// to the public QUIC clients via the SNI router's PacketConn.
func (r *quicSNIRouter) writeBackLoop(leaseID string, broker *quicBroker) {
	for {
		select {
		case <-r.done:
			return
		case <-broker.Done():
			return
		case frame := <-broker.Incoming():
			addr, ok := broker.LookupFlowAddr(frame.FlowID)
			if !ok {
				continue
			}
			_, _ = r.conn.WriteTo(frame.Payload, addr)
		}
	}
}

func (r *quicSNIRouter) close() error {
	var closeErr error
	r.closeOnce.Do(func() {
		close(r.done)
		closeErr = r.conn.Close()
	})
	return closeErr
}
