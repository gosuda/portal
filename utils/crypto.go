package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/gosuda/portal/v2/types"
	"golang.org/x/crypto/sha3"
)

func NormalizeEVMAddress(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("address is required")
	}
	hexPart := TrimHexPrefix(trimmed)
	if hexPart == trimmed {
		return "", errors.New("address must start with 0x")
	}
	if len(hexPart) != 40 {
		return "", errors.New("address must be 20 bytes")
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		return "", errors.New("address must be hex encoded")
	}

	lowerHex := strings.ToLower(hexPart)
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write([]byte(lowerHex))
	hash := hasher.Sum(nil)

	var builder strings.Builder
	builder.Grow(len(lowerHex))
	for idx, ch := range lowerHex {
		if ch >= '0' && ch <= '9' {
			builder.WriteRune(ch)
			continue
		}

		nibble := hash[idx/2]
		if idx%2 == 0 {
			nibble >>= 4
		} else {
			nibble &= 0x0f
		}
		if nibble > 7 {
			builder.WriteRune(ch - ('a' - 'A'))
			continue
		}
		builder.WriteRune(ch)
	}

	checksummed := builder.String()
	if hexPart != lowerHex && hexPart != strings.ToUpper(hexPart) && hexPart != checksummed {
		return "", errors.New("address checksum is invalid")
	}
	return "0x" + checksummed, nil
}

func AddressFromCompressedPublicKeyHex(rawPublicKey string) (string, error) {
	publicKey, err := ParseSecp256k1PublicKeyHex(rawPublicKey)
	if err != nil {
		return "", err
	}

	uncompressed := publicKey.SerializeUncompressed()
	if len(uncompressed) != 65 || uncompressed[0] != 0x04 {
		return "", errors.New("invalid uncompressed secp256k1 public key")
	}

	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write(uncompressed[1:])
	hash := hasher.Sum(nil)

	return NormalizeEVMAddress("0x" + hex.EncodeToString(hash[len(hash)-20:]))
}

func SignEthereumPersonalMessage(message, privateKeyHex string) (string, error) {
	privateKey, _, err := ParseSecp256k1PrivateKeyHex(privateKeyHex, false)
	if err != nil {
		return "", err
	}

	data := []byte(message)
	prefix := []byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d", len(data)))
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write(prefix)
	_, _ = hasher.Write(data)
	hash := hasher.Sum(nil)

	compactSignature := ecdsa.SignCompact(privateKey, hash, false)
	if len(compactSignature) != 65 {
		return "", errors.New("invalid compact signature length")
	}

	signature := make([]byte, 65)
	copy(signature[:32], compactSignature[1:33])
	copy(signature[32:64], compactSignature[33:65])
	signature[64] = compactSignature[0]
	return "0x" + hex.EncodeToString(signature), nil
}

func ResolveSecp256k1Identity(rawPrivateKey string) (types.Identity, error) {
	privateKeyHex := strings.TrimSpace(rawPrivateKey)
	if privateKeyHex == "" {
		privateKey, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			return types.Identity{}, fmt.Errorf("generate secp256k1 private key: %w", err)
		}
		privateKeyHex = hex.EncodeToString(privateKey.Serialize())
	}

	privateKey, normalizedKeyHex, err := ParseSecp256k1PrivateKeyHex(privateKeyHex, true)
	if err != nil {
		return types.Identity{}, err
	}

	publicKeyHex := hex.EncodeToString(privateKey.PubKey().SerializeCompressed())
	address, err := AddressFromCompressedPublicKeyHex(publicKeyHex)
	if err != nil {
		return types.Identity{}, err
	}

	return types.Identity{
		Address:    address,
		PublicKey:  publicKeyHex,
		PrivateKey: normalizedKeyHex,
	}, nil
}

func SignSHA256Secp256k1DER(payload []byte, privateKeyHex string) (string, error) {
	privateKey, _, err := ParseSecp256k1PrivateKeyHex(privateKeyHex, false)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(payload)
	signature := ecdsa.Sign(privateKey, hash[:])
	return hex.EncodeToString(signature.Serialize()), nil
}

func VerifySHA256Secp256k1DER(payload []byte, publicKeyHex, signatureHex string) error {
	pubKey, err := ParseSecp256k1PublicKeyHex(publicKeyHex)
	if err != nil {
		return err
	}

	sigText := strings.TrimSpace(signatureHex)
	if sigText == "" {
		return errors.New("signature is required")
	}
	sigText = TrimHexPrefix(sigText)

	sigBytes, err := hex.DecodeString(sigText)
	if err != nil {
		return errors.New("signature must be hex encoded")
	}
	signature, err := ecdsa.ParseDERSignature(sigBytes)
	if err != nil {
		return fmt.Errorf("parse signature: %w", err)
	}

	hash := sha256.Sum256(payload)
	if !signature.Verify(hash[:], pubKey) {
		return errors.New("signature is invalid")
	}
	return nil
}

func ParseSecp256k1PublicKeyHex(raw string) (*secp256k1.PublicKey, error) {
	publicKeyHex := strings.TrimSpace(raw)
	if publicKeyHex == "" {
		return nil, errors.New("public key is required")
	}
	publicKeyHex = TrimHexPrefix(publicKeyHex)

	decoded, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return nil, errors.New("public key must be hex encoded")
	}

	publicKey, err := secp256k1.ParsePubKey(decoded)
	if err != nil {
		return nil, errors.New("invalid secp256k1 public key")
	}
	return publicKey, nil
}

func ParseSecp256k1PrivateKeyHex(raw string, requireNonZero bool) (*secp256k1.PrivateKey, string, error) {
	privateKeyHex := strings.TrimSpace(raw)
	if privateKeyHex == "" {
		return nil, "", errors.New("private key is required")
	}
	privateKeyHex = TrimHexPrefix(privateKeyHex)

	decoded, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return nil, "", errors.New("secp256k1 private key must be hex encoded")
	}
	if len(decoded) != secp256k1.PrivKeyBytesLen {
		return nil, "", fmt.Errorf("secp256k1 private key must be %d bytes", secp256k1.PrivKeyBytesLen)
	}
	if !requireNonZero {
		key := secp256k1.PrivKeyFromBytes(decoded)
		if key == nil {
			return nil, "", errors.New("invalid secp256k1 private key")
		}
		return key, privateKeyHex, nil
	}

	isZero := true
	for _, b := range decoded {
		if b != 0 {
			isZero = false
			break
		}
	}
	if isZero {
		return nil, "", errors.New("secp256k1 private key must not be zero")
	}
	key := secp256k1.PrivKeyFromBytes(decoded)
	if key == nil {
		return nil, "", errors.New("invalid secp256k1 private key")
	}
	return key, privateKeyHex, nil
}
