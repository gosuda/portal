package cryptoops

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
)

var _id_magic = []byte("RDVERB_PROTOCOL_VER_01_SHA256_ID")
var _base32_encoding = base32.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567").WithPadding(base32.NoPadding)

func DeriveID(publickey ed25519.PublicKey) string {
	h := hmac.New(sha256.New, _id_magic)
	h.Write(publickey)
	return _base32_encoding.EncodeToString(h.Sum(nil))
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
