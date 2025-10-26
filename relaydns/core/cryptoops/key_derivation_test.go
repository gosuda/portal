package cryptoops

import (
	"bytes"
	"crypto/rand"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

// TestKeyDerivation tests the key derivation functions
func TestKeyDerivation(t *testing.T) {
	// Generate two X25519 key pairs
	clientPriv, clientPub, err := generateX25519KeyPair()
	if err != nil {
		t.Fatalf("Failed to generate client key pair: %v", err)
	}

	serverPriv, serverPub, err := generateX25519KeyPair()
	if err != nil {
		t.Fatalf("Failed to generate server key pair: %v", err)
	}

	// Generate nonces
	clientNonce := make([]byte, nonceSize)
	serverNonce := make([]byte, nonceSize)
	if _, err := rand.Read(clientNonce); err != nil {
		t.Fatalf("Failed to generate client nonce: %v", err)
	}
	if _, err := rand.Read(serverNonce); err != nil {
		t.Fatalf("Failed to generate server nonce: %v", err)
	}

	// Create handshakers
	clientCred, err := NewCredential()
	if err != nil {
		t.Fatalf("Failed to create client credential: %v", err)
	}
	serverCred, err := NewCredential()
	if err != nil {
		t.Fatalf("Failed to create server credential: %v", err)
	}

	clientHandshaker := NewHandshaker(clientCred)
	serverHandshaker := NewHandshaker(serverCred)

	// Derive keys
	clientEncryptKey, clientDecryptKey, err := clientHandshaker.deriveClientSessionKeys(
		clientPriv, clientPub, serverPub, clientNonce, serverNonce)
	if err != nil {
		t.Fatalf("Failed to derive client session keys: %v", err)
	}

	serverEncryptKey, serverDecryptKey, err := serverHandshaker.deriveServerSessionKeys(
		serverPriv, serverPub, clientPub, clientNonce, serverNonce)
	if err != nil {
		t.Fatalf("Failed to derive server session keys: %v", err)
	}

	// Check that keys match correctly
	// Client encrypts, server decrypts
	if !bytes.Equal(clientEncryptKey, serverDecryptKey) {
		t.Error("Client encrypt key doesn't match server decrypt key")
	}

	// Server encrypts, client decrypts
	if !bytes.Equal(serverEncryptKey, clientDecryptKey) {
		t.Error("Server encrypt key doesn't match client decrypt key")
	}

	// Test actual encryption/decryption
	testMessage := []byte("Hello, world!")

	// Client encrypts
	clientAEAD, err := chacha20poly1305.New(clientEncryptKey)
	if err != nil {
		t.Fatalf("Failed to create client AEAD: %v", err)
	}

	clientEncryptNonce := make([]byte, nonceSize)
	copy(clientEncryptNonce, clientNonce) // Start with client nonce
	encrypted := clientAEAD.Seal(nil, clientEncryptNonce, testMessage, nil)

	// Server decrypts
	serverAEAD, err := chacha20poly1305.New(serverDecryptKey)
	if err != nil {
		t.Fatalf("Failed to create server AEAD: %v", err)
	}

	serverDecryptNonce := make([]byte, nonceSize)
	copy(serverDecryptNonce, clientNonce) // Use same nonce as client
	decrypted, err := serverAEAD.Open(nil, serverDecryptNonce, encrypted, nil)
	if err != nil {
		t.Fatalf("Server failed to decrypt: %v", err)
	}

	if !bytes.Equal(decrypted, testMessage) {
		t.Errorf("Decrypted message %q doesn't match original %q", decrypted, testMessage)
	}

	// Server encrypts response
	serverEncryptNonce := make([]byte, nonceSize)
	copy(serverEncryptNonce, serverNonce) // Start with server nonce
	responseMessage := []byte("Hello back!")
	encryptedResponse := serverAEAD.Seal(nil, serverEncryptNonce, responseMessage, nil)

	// Client decrypts response
	clientDecryptNonce := make([]byte, nonceSize)
	copy(clientDecryptNonce, serverNonce) // Use same nonce as server
	decryptedResponse, err := clientAEAD.Open(nil, clientDecryptNonce, encryptedResponse, nil)
	if err != nil {
		t.Fatalf("Client failed to decrypt response: %v", err)
	}

	if !bytes.Equal(decryptedResponse, responseMessage) {
		t.Errorf("Decrypted response %q doesn't match original %q", decryptedResponse, responseMessage)
	}
}
