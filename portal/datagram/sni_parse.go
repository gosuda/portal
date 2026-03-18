package datagram

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/hkdf"
)

var quicV1InitialSalt = []byte{
	0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3,
	0x4d, 0x17, 0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad,
	0xcc, 0xbb, 0x7f, 0x0a,
}

var errNotQUICInitial = errors.New("not a quic initial packet")
var errSNINotFound = errors.New("sni not found in quic initial")

func ParseQUICInitialSNI(packet []byte) (string, error) {
	if len(packet) < 5 {
		return "", errNotQUICInitial
	}

	firstByte := packet[0]
	if firstByte&0x80 == 0 {
		return "", errNotQUICInitial
	}

	packetType := (firstByte & 0x30) >> 4
	if packetType != 0 {
		return "", errNotQUICInitial
	}

	version := binary.BigEndian.Uint32(packet[1:5])
	if version == 0 {
		return "", errNotQUICInitial
	}

	offset := 5
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

	if offset >= len(packet) {
		return "", errNotQUICInitial
	}
	scidLen := int(packet[offset])
	offset++
	offset += scidLen
	if offset > len(packet) {
		return "", errNotQUICInitial
	}

	tokenLen, n := readVarint(packet[offset:])
	if n <= 0 {
		return "", errNotQUICInitial
	}
	offset += n + int(tokenLen)
	if offset > len(packet) {
		return "", errNotQUICInitial
	}

	payloadLen, n := readVarint(packet[offset:])
	if n <= 0 {
		return "", errNotQUICInitial
	}
	offset += n
	_ = payloadLen

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

	pnOffset := offset
	sampleOffset := pnOffset + 4
	if sampleOffset+16 > len(packet) {
		return "", errNotQUICInitial
	}
	sample := packet[sampleOffset : sampleOffset+16]

	block, err := aes.NewCipher(hp)
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	mask := make([]byte, aes.BlockSize)
	block.Encrypt(mask, sample)

	unmaskedFirst := packet[0] ^ (mask[0] & 0x0f)
	pnLength := int(unmaskedFirst&0x03) + 1

	pnBytes := make([]byte, pnLength)
	for i := range pnLength {
		pnBytes[i] = packet[pnOffset+i] ^ mask[1+i]
	}

	var pn uint32
	for _, b := range pnBytes {
		pn = (pn << 8) | uint32(b)
	}

	nonce := make([]byte, len(iv))
	copy(nonce, iv)
	for i := range nonce {
		if i >= len(nonce)-4 {
			nonce[i] ^= byte(pn >> (8 * (len(nonce) - 1 - i)))
		}
	}

	payloadOffset := pnOffset + pnLength
	if payloadOffset >= len(packet) {
		return "", errNotQUICInitial
	}

	aad := make([]byte, payloadOffset)
	copy(aad, packet[:payloadOffset])
	aad[0] = unmaskedFirst
	copy(aad[pnOffset:], pnBytes)

	aeadBlock, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("aead cipher: %w", err)
	}
	aead, err := cipher.NewGCM(aeadBlock)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}

	plaintext, err := aead.Open(nil, nonce, packet[payloadOffset:], aad)
	if err != nil {
		return "", fmt.Errorf("decrypt initial payload: %w", err)
	}

	return extractSNIFromCryptoFrames(plaintext)
}

func extractSNIFromCryptoFrames(frames []byte) (string, error) {
	offset := 0
	for offset < len(frames) {
		frameType := frames[offset]
		offset++

		switch frameType {
		case 0x00, 0x01:
			continue
		case 0x06:
			_, n := readVarint(frames[offset:])
			if n <= 0 {
				return "", errSNINotFound
			}
			offset += n

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
			return "", errSNINotFound
		}
	}
	return "", errSNINotFound
}

func parseTLSClientHelloSNI(data []byte) (string, error) {
	if len(data) < 4 || data[0] != 0x01 {
		return "", errSNINotFound
	}
	msgLen := int(data[1])<<16 | int(data[2])<<8 | int(data[3])
	if len(data) < 4+msgLen {
		return "", errSNINotFound
	}
	body := data[4 : 4+msgLen]

	if len(body) < 34 {
		return "", errSNINotFound
	}
	offset := 34

	if offset >= len(body) {
		return "", errSNINotFound
	}
	sessionIDLen := int(body[offset])
	offset += 1 + sessionIDLen

	if offset+2 > len(body) {
		return "", errSNINotFound
	}
	cipherSuitesLen := int(body[offset])<<8 | int(body[offset+1])
	offset += 2 + cipherSuitesLen

	if offset >= len(body) {
		return "", errSNINotFound
	}
	compMethodsLen := int(body[offset])
	offset += 1 + compMethodsLen

	if offset+2 > len(body) {
		return "", errSNINotFound
	}
	extensionsLen := int(body[offset])<<8 | int(body[offset+1])
	offset += 2

	extEnd := min(offset+extensionsLen, len(body))

	for offset+4 <= extEnd {
		extType := int(body[offset])<<8 | int(body[offset+1])
		extLen := int(body[offset+2])<<8 | int(body[offset+3])
		offset += 4

		if offset+extLen > extEnd {
			return "", errSNINotFound
		}
		if extType == 0x0000 {
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
	listLen := int(data[0])<<8 | int(data[1])
	offset := 2
	end := min(offset+listLen, len(data))

	for offset+3 <= end {
		nameType := data[offset]
		nameLen := int(data[offset+1])<<8 | int(data[offset+2])
		offset += 3
		if nameType == 0x00 {
			if offset+nameLen > end {
				return "", errSNINotFound
			}
			return string(data[offset : offset+nameLen]), nil
		}
		offset += nameLen
	}
	return "", errSNINotFound
}

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

func hkdfLabel(label []byte, length int) []byte {
	fullLabel := append([]byte("tls13 "), label...)
	out := make([]byte, 2+1+len(fullLabel)+1)
	out[0] = byte(length >> 8)
	out[1] = byte(length)
	out[2] = byte(len(fullLabel))
	copy(out[3:], fullLabel)
	out[3+len(fullLabel)] = 0
	return out
}

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
