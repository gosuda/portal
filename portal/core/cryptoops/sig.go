package cryptoops

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"errors"

	"golang.org/x/crypto/curve25519"
)

var _id_magic = []byte("RDVERB_PROTOCOL_VER_01_SHA256_ID")
var _base32_encoding = base32.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567").WithPadding(base32.NoPadding)

func DeriveID(publickey ed25519.PublicKey) string {
	h := hmac.New(sha256.New, _id_magic)
	h.Write(publickey)
	hash := h.Sum(nil)
	return _base32_encoding.EncodeToString(hash[:16])
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
	// Clamp per RFC 7748
	h[0] &= 248
	h[31] &= 127
	h[31] |= 64
	key := make([]byte, curve25519.ScalarSize)
	copy(key, h[:curve25519.ScalarSize])
	return key
}

// X25519PublicKey derives the X25519 public key corresponding to X25519PrivateKey.
func (c *Credential) X25519PublicKey() []byte {
	priv := c.X25519PrivateKey()
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		panic("x25519 scalar base mult: " + err.Error())
	}
	return pub
}
