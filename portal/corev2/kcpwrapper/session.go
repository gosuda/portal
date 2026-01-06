package kcpwrapper

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	SessionIDSize = 16
	KeySize       = 32
	NonceSize     = 12
)

type Session struct {
	mu         sync.RWMutex
	sessionID  [SessionIDSize]byte
	writeKey   [KeySize]byte
	readKey    [KeySize]byte
	writeNonce [NonceSize]byte
	readNonce  [NonceSize]byte
	keyPhase   bool
}

func NewSession(sessionID [SessionIDSize]byte) *Session {
	return &Session{
		sessionID: sessionID,
		keyPhase:  false,
	}
}

func (s *Session) SetKeys(writeKey, readKey [KeySize]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.writeKey = writeKey
	s.readKey = readKey
}

func (s *Session) RotateKeys(newWrite, newRead [KeySize]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.writeKey = newWrite
	s.readKey = newRead
	s.keyPhase = !s.keyPhase
	s.writeNonce = [NonceSize]byte{}
	s.readNonce = [NonceSize]byte{}
}

func (s *Session) Write(p []byte) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(p) == 0 {
		return 0, nil
	}

	aead, err := chacha20poly1305.NewX(s.writeKey[:])
	if err != nil {
		return 0, err
	}

	nonce := make([]byte, NonceSize)
	copy(nonce, s.writeNonce[:])

	incNonce(&s.writeNonce)

	encrypted := aead.Seal(nil, nonce, nil, p)

	if len(encrypted) > len(p)+16 {
		return 0, io.ErrShortBuffer
	}

	return len(encrypted), nil
}

func (s *Session) Read(p []byte) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(p) == 0 {
		return 0, nil
	}

	aead, err := chacha20poly1305.NewX(s.readKey[:])
	if err != nil {
		return 0, err
	}

	if len(p) < 16 {
		return 0, errors.New("ciphertext too short")
	}

	nonce := make([]byte, NonceSize)
	copy(nonce, s.readNonce[:])

	incNonce(&s.readNonce)

	plaintext, err := aead.Open(nil, nonce, p, nil)
	if err != nil {
		return 0, err
	}

	copy(p, plaintext)
	return len(plaintext), nil
}

func incNonce(nonce *[NonceSize]byte) {
	for i := 0; i < NonceSize; i++ {
		nonce[i]++
		if nonce[i] != 0 {
			break
		}
	}
}

func GenerateNonce() [NonceSize]byte {
	var nonce [NonceSize]byte
	_, err := rand.Read(nonce[:])
	if err != nil {
		panic(err)
	}
	return nonce
}

func DeriveKeys(masterKey []byte, label string, context []byte) ([KeySize]byte, [KeySize]byte) {
	if len(masterKey) != KeySize {
		panic("master key must be 32 bytes")
	}

	info := append([]byte(label), context...)
	h := hkdf.New(sha256.New, masterKey, nil, info)

	var writeKey, readKey [KeySize]byte
	io.ReadFull(h, writeKey[:])
	io.ReadFull(h, readKey[:])

	return writeKey, readKey
}

func DeriveSessionID(masterKey []byte, sessionID [SessionIDSize]byte) [SessionIDSize]byte {
	if len(masterKey) != KeySize {
		panic("master key must be 32 bytes")
	}

	h := hkdf.New(sha256.New, masterKey, nil, sessionID[:])

	var derived [SessionIDSize]byte
	io.ReadFull(h, derived[:])

	return derived
}
