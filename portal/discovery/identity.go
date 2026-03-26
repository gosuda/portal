package discovery

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/sha3"
)

type Identity struct {
	Generated  bool   `json:"generated,omitempty"`
	Address    string `json:"address"`
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
}

func AddressFromCompressedPublicKeyHex(rawPublicKey string) (string, error) {
	publicKeyHex := strings.TrimSpace(rawPublicKey)
	if publicKeyHex == "" {
		return "", errors.New("public key is required")
	}
	if strings.HasPrefix(strings.ToLower(publicKeyHex), "0x") {
		publicKeyHex = publicKeyHex[2:]
	}

	decoded, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return "", errors.New("public key must be hex encoded")
	}

	publicKey, err := secp256k1.ParsePubKey(decoded)
	if err != nil {
		return "", errors.New("invalid secp256k1 public key")
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

func NormalizeEVMAddress(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("address is required")
	}
	if !strings.HasPrefix(strings.ToLower(trimmed), "0x") {
		return "", errors.New("address must start with 0x")
	}

	hexPart := trimmed[2:]
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

func ResolveIdentity(rawPrivateKey string) (Identity, error) {
	privateKeyHex := strings.TrimSpace(rawPrivateKey)
	generated := false
	if privateKeyHex == "" {
		privateKey, err := secp256k1.GeneratePrivateKey()
		if err != nil {
			return Identity{}, fmt.Errorf("generate secp256k1 private key: %w", err)
		}
		privateKeyHex = hex.EncodeToString(privateKey.Serialize())
		generated = true
	}
	if strings.HasPrefix(strings.ToLower(privateKeyHex), "0x") {
		privateKeyHex = privateKeyHex[2:]
	}

	decoded, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return Identity{}, errors.New("secp256k1 private key must be hex encoded")
	}
	if len(decoded) != secp256k1.PrivKeyBytesLen {
		return Identity{}, fmt.Errorf("secp256k1 private key must be %d bytes", secp256k1.PrivKeyBytesLen)
	}

	isZero := true
	for _, b := range decoded {
		if b != 0 {
			isZero = false
			break
		}
	}
	if isZero {
		return Identity{}, errors.New("secp256k1 private key must not be zero")
	}

	privateKey := secp256k1.PrivKeyFromBytes(decoded)
	if privateKey == nil {
		return Identity{}, errors.New("invalid secp256k1 private key")
	}

	publicKeyHex := hex.EncodeToString(privateKey.PubKey().SerializeCompressed())
	address, err := AddressFromCompressedPublicKeyHex(publicKeyHex)
	if err != nil {
		return Identity{}, err
	}

	return Identity{
		Generated:  generated,
		Address:    address,
		PublicKey:  publicKeyHex,
		PrivateKey: privateKeyHex,
	}, nil
}
