package utils

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"golang.org/x/crypto/curve25519"
)

func NormalizeWireGuardPrivateKey(raw string) (string, error) {
	key, err := decodeWireGuardKey(raw)
	if err != nil {
		return "", err
	}
	clampWireGuardPrivateKey(&key)
	return base64.StdEncoding.EncodeToString(key[:]), nil
}

func WireGuardPublicKeyFromPrivate(raw string) (string, error) {
	privateKey, err := decodeWireGuardKey(raw)
	if err != nil {
		return "", err
	}
	clampWireGuardPrivateKey(&privateKey)
	var publicKey [32]byte
	curve25519.ScalarBaseMult(&publicKey, &privateKey)
	return base64.StdEncoding.EncodeToString(publicKey[:]), nil
}

func WireGuardListenPort(rawEndpoint string) (int, error) {
	endpoint := strings.TrimSpace(rawEndpoint)
	if endpoint == "" {
		return 0, errors.New("wireguard endpoint is required")
	}
	_, portText, err := net.SplitHostPort(endpoint)
	if err != nil {
		return 0, errors.New("wireguard endpoint must be host:port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return 0, errors.New("wireguard endpoint port is invalid")
	}
	return port, nil
}

func DeriveWireGuardOverlayIPv4(publicKey string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKey))
	if err != nil {
		return "", errors.New("wireguard public key must be base64 encoded")
	}
	if len(decoded) != 32 {
		return "", errors.New("wireguard public key must be 32 bytes")
	}

	sum := sha256.Sum256(decoded)
	return netip.AddrFrom4([4]byte{
		100,
		64 + (sum[0] & 0x3f),
		sum[1],
		1 + (sum[2] % 254),
	}).String(), nil
}

func WireGuardKeyHex(raw string) (string, error) {
	key, err := decodeWireGuardKey(raw)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(key[:]), nil
}

func decodeWireGuardKey(raw string) ([32]byte, error) {
	var key [32]byte
	value := strings.TrimSpace(raw)
	if value == "" {
		return key, errors.New("wireguard key is required")
	}

	var decoded []byte
	var err error
	if len(value) == 64 && !strings.Contains(value, "=") {
		decoded, err = hex.DecodeString(value)
	} else {
		decoded, err = base64.StdEncoding.DecodeString(value)
	}
	if err != nil {
		return key, errors.New("wireguard key must be base64 or hex encoded")
	}
	if len(decoded) != len(key) {
		return key, errors.New("wireguard key must be 32 bytes")
	}
	copy(key[:], decoded)
	return key, nil
}

func clampWireGuardPrivateKey(key *[32]byte) {
	key[0] &= 248
	key[31] = (key[31] & 127) | 64
}
