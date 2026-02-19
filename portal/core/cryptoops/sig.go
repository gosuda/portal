package cryptoops

import (
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"
)

type Credential struct {
	x25519PrivateKey []byte
	x25519PublicKey  []byte
}

func NewCredentialFromPrivateKey(privateKey []byte) (*Credential, error) {
	if len(privateKey) != 32 {
		return nil, errors.New("invalid private key length")
	}

	curve := ecdh.X25519()
	key, err := curve.NewPrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	return &Credential{
		x25519PrivateKey: append([]byte(nil), key.Bytes()...),
		x25519PublicKey:  append([]byte(nil), key.PublicKey().Bytes()...),
	}, nil
}

func NewCredential() (*Credential, error) {
	curve := ecdh.X25519()
	key, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}

	return &Credential{
		x25519PrivateKey: append([]byte(nil), key.Bytes()...),
		x25519PublicKey:  append([]byte(nil), key.PublicKey().Bytes()...),
	}, nil
}

func (c *Credential) X25519PrivateKey() []byte {
	return append([]byte(nil), c.x25519PrivateKey...)
}

func (c *Credential) X25519PublicKey() []byte {
	return append([]byte(nil), c.x25519PublicKey...)
}

func (c *Credential) ID() string {
	return DeriveID(c.x25519PublicKey)
}

func (c *Credential) PublicKey() []byte {
	return c.X25519PublicKey()
}

func DeriveID(publicKey []byte) string {
	return deriveConnectionID(publicKey)
}
