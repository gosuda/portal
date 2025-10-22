package sdk

import (
	"crypto/aes"
	"crypto/cipher"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// Envelope is a simple password-derived AES-GCM container used by examples.
// Fields are base64-encoded to keep the JSON compact and interoperable with browser JS.
type Envelope struct {
	Salt  string `json:"salt"`
	Nonce string `json:"nonce"`
	Ct    string `json:"ct"`
}

// EnsurePSKAtPath loads a pre-shared key (PSK) from the given file path.
// If missing/empty, generates a random base64url string, writes it, and returns it.
func EnsurePSKAtPath(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			return s, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	buf := make([]byte, 16)
	if _, err := crand.Read(buf); err != nil {
		return "", err
	}
	psk := base64.RawURLEncoding.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(psk+"\n"), 0o600); err != nil {
		return "", err
	}
	return psk, nil
}

func deriveKey(psk string, salt []byte) []byte {
	return pbkdf2.Key([]byte(psk), salt, 100000, 32, sha256.New)
}

// DecryptEnvelope decrypts the given envelope using a key derived from psk+salt.
func DecryptEnvelope(psk string, env Envelope) ([]byte, error) {
	salt, err := base64.StdEncoding.DecodeString(env.Salt)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(env.Ct)
	if err != nil {
		return nil, err
	}
	if len(nonce) != 12 {
		return nil, errors.New("bad nonce")
	}
	key := deriveKey(psk, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ct, nil)
}

// EncryptEnvelope encrypts pt with a key derived from psk and a fresh random salt/nonce.
func EncryptEnvelope(psk string, pt []byte) (Envelope, error) {
	var env Envelope
	salt := make([]byte, 16)
	if _, err := crand.Read(salt); err != nil {
		return env, err
	}
	nonce := make([]byte, 12)
	if _, err := crand.Read(nonce); err != nil {
		return env, err
	}
	key := deriveKey(psk, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return env, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return env, err
	}
	ct := aead.Seal(nil, nonce, pt, nil)
	env.Salt = base64.StdEncoding.EncodeToString(salt)
	env.Nonce = base64.StdEncoding.EncodeToString(nonce)
	env.Ct = base64.StdEncoding.EncodeToString(ct)
	return env, nil
}
