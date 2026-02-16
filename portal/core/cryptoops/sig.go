package cryptoops

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"errors"
)

const idMagic = "RDVERB_PROTOCOL_VER_01_SHA256_ID"

func DeriveID(publickey ed25519.PublicKey) string {
	h := hmac.New(sha256.New, []byte(idMagic))
	h.Write(publickey)
	hash := h.Sum(nil)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash[:16])
}

type Credential struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	id         string
}

func NewCredentialFromPrivateKey(privateKey ed25519.PrivateKey) (*Credential, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid private key length")
	}

	publicKey := privateKey.Public().(ed25519.PublicKey)
	return &Credential{
		privateKey: privateKey,
		publicKey:  publicKey,
		id:         DeriveID(publicKey),
	}, nil
}

func NewCredential() (*Credential, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}

	return NewCredentialFromPrivateKey(privateKey)
}

func (c *Credential) ID() string {
	return c.id
}

func (c *Credential) Sign(data []byte) []byte {
	return ed25519.Sign(c.privateKey, data)
}

func (c *Credential) Verify(data, sig []byte) bool {
	if len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(c.publicKey, data, sig)
}

func (c *Credential) PublicKey() ed25519.PublicKey {
	return c.publicKey
}

func (c *Credential) PrivateKey() ed25519.PrivateKey {
	return c.privateKey
}

// X25519PrivateKey derives an X25519 private key from the Ed25519 seed.
// This follows the standard Ed25519→X25519 conversion: SHA-512(seed)[:32] with clamping.
func (c *Credential) X25519PrivateKey() []byte {
	h := sha512.Sum512(c.privateKey.Seed())
	defer wipeMemory(h[:])

	// Clamp per RFC 7748
	h[0] &= 248
	h[31] &= 127
	h[31] |= 64
	key := make([]byte, 32)
	copy(key, h[:32])
	return key
}

// X25519PublicKey derives the X25519 public key corresponding to X25519PrivateKey.
func (c *Credential) X25519PublicKey() []byte {
	curve := ecdh.X25519()
	priv, err := curve.NewPrivateKey(c.X25519PrivateKey())
	if err != nil {
		panic("x25519 private key: " + err.Error())
	}
	return priv.PublicKey().Bytes()
}
