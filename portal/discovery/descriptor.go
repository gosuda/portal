package discovery

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	secp256k1ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"

	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

func NormalizeDescriptor(desc types.RelayDescriptor) (types.RelayDescriptor, error) {
	desc.RelayID = strings.TrimSpace(desc.RelayID)
	desc.OwnerAddress = strings.TrimSpace(desc.OwnerAddress)
	desc.SignerPublicKey = strings.ToLower(strings.TrimSpace(desc.SignerPublicKey))
	desc.APIHTTPSAddr = strings.TrimSpace(desc.APIHTTPSAddr)
	desc.IngressTLSAddr = strings.TrimSpace(desc.IngressTLSAddr)
	desc.WireGuardPublicKey = strings.TrimSpace(desc.WireGuardPublicKey)
	desc.WireGuardEndpoint = strings.TrimSpace(desc.WireGuardEndpoint)
	desc.OverlayIPv4 = strings.TrimSpace(desc.OverlayIPv4)
	desc.StatusState = strings.TrimSpace(desc.StatusState)
	desc.Region = strings.TrimSpace(desc.Region)
	desc.Country = strings.TrimSpace(desc.Country)
	desc.DescriptorSignature = strings.ToLower(strings.TrimSpace(desc.DescriptorSignature))
	if !desc.IssuedAt.IsZero() {
		desc.IssuedAt = desc.IssuedAt.UTC()
	}
	if !desc.ExpiresAt.IsZero() {
		desc.ExpiresAt = desc.ExpiresAt.UTC()
	}
	if !desc.LastMITMDetectedAt.IsZero() {
		desc.LastMITMDetectedAt = desc.LastMITMDetectedAt.UTC()
	}

	if desc.APIHTTPSAddr != "" {
		normalized, err := utils.NormalizeRelayURL(desc.APIHTTPSAddr)
		if err != nil {
			return types.RelayDescriptor{}, fmt.Errorf("normalize api https addr: %w", err)
		}
		desc.APIHTTPSAddr = normalized
		if desc.RelayID == "" {
			desc.RelayID = normalized
		}
	}
	if desc.OwnerAddress != "" {
		address, err := NormalizeEVMAddress(desc.OwnerAddress)
		if err != nil {
			return types.RelayDescriptor{}, fmt.Errorf("normalize owner address: %w", err)
		}
		desc.OwnerAddress = address
	}
	if len(desc.OverlayCIDRs) > 0 {
		normalized, err := NormalizeOverlayCIDRs(desc.OverlayCIDRs)
		if err != nil {
			return types.RelayDescriptor{}, err
		}
		desc.OverlayCIDRs = normalized
	}

	if !desc.SupportsOverlayPeer {
		desc.WireGuardPublicKey = ""
		desc.WireGuardEndpoint = ""
		desc.OverlayIPv4 = ""
		desc.OverlayCIDRs = nil
	}

	return desc, nil
}

func CanonicalDescriptorPayload(desc types.RelayDescriptor) ([]byte, error) {
	normalized, err := NormalizeDescriptor(desc)
	if err != nil {
		return nil, err
	}
	normalized.DescriptorSignature = ""
	return json.Marshal(normalized)
}

func SignDescriptor(desc types.RelayDescriptor, privateKeyHex string) (string, error) {
	keyHex := strings.TrimSpace(privateKeyHex)
	if keyHex == "" {
		return "", errors.New("private key is required")
	}
	if strings.HasPrefix(strings.ToLower(keyHex), "0x") {
		keyHex = keyHex[2:]
	}
	decoded, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", errors.New("private key must be hex encoded")
	}
	if len(decoded) != secp256k1.PrivKeyBytesLen {
		return "", fmt.Errorf("private key must be %d bytes", secp256k1.PrivKeyBytesLen)
	}

	payload, err := CanonicalDescriptorPayload(desc)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(payload)
	privateKey := secp256k1.PrivKeyFromBytes(decoded)
	signature := secp256k1ecdsa.Sign(privateKey, hash[:])
	return hex.EncodeToString(signature.Serialize()), nil
}

func SignedDescriptor(desc types.RelayDescriptor, privateKeyHex string) (types.RelayDescriptor, error) {
	normalized, err := NormalizeDescriptor(desc)
	if err != nil {
		return types.RelayDescriptor{}, err
	}
	signature, err := SignDescriptor(normalized, privateKeyHex)
	if err != nil {
		return types.RelayDescriptor{}, err
	}
	normalized.DescriptorSignature = signature
	return normalized, nil
}

func VerifyDescriptor(desc types.RelayDescriptor) error {
	normalized, err := NormalizeDescriptor(desc)
	if err != nil {
		return err
	}
	if normalized.DescriptorSignature == "" {
		return errors.New("descriptor signature is required")
	}
	if normalized.SignerPublicKey == "" {
		return errors.New("signer public key is required")
	}

	pubKeyBytes, err := hex.DecodeString(normalized.SignerPublicKey)
	if err != nil {
		return errors.New("signer public key must be hex encoded")
	}
	pubKey, err := secp256k1.ParsePubKey(pubKeyBytes)
	if err != nil {
		return errors.New("invalid secp256k1 signer public key")
	}

	sigBytes, err := hex.DecodeString(normalized.DescriptorSignature)
	if err != nil {
		return errors.New("descriptor signature must be hex encoded")
	}
	signature, err := secp256k1ecdsa.ParseDERSignature(sigBytes)
	if err != nil {
		return fmt.Errorf("parse descriptor signature: %w", err)
	}

	payload, err := CanonicalDescriptorPayload(normalized)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(payload)
	if !signature.Verify(hash[:], pubKey) {
		return errors.New("descriptor signature is invalid")
	}
	return nil
}

func ValidateDescriptor(desc types.RelayDescriptor, now time.Time) (types.RelayDescriptor, error) {
	normalized, err := NormalizeDescriptor(desc)
	if err != nil {
		return types.RelayDescriptor{}, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	switch {
	case normalized.RelayID == "":
		return types.RelayDescriptor{}, errors.New("relay_id is required")
	case normalized.OwnerAddress == "":
		return types.RelayDescriptor{}, errors.New("owner_address is required")
	case normalized.SignerPublicKey == "":
		return types.RelayDescriptor{}, errors.New("signer_public_key is required")
	case normalized.APIHTTPSAddr == "":
		return types.RelayDescriptor{}, errors.New("api_https_addr is required")
	case normalized.Sequence == 0:
		return types.RelayDescriptor{}, errors.New("sequence is required")
	case normalized.Version == 0:
		return types.RelayDescriptor{}, errors.New("version is required")
	case normalized.IssuedAt.IsZero():
		return types.RelayDescriptor{}, errors.New("issued_at is required")
	case normalized.ExpiresAt.IsZero():
		return types.RelayDescriptor{}, errors.New("expires_at is required")
	case normalized.ExpiresAt.Before(now):
		return types.RelayDescriptor{}, errors.New("descriptor expired")
	case normalized.IssuedAt.After(normalized.ExpiresAt):
		return types.RelayDescriptor{}, errors.New("issued_at must be before expires_at")
	}

	derivedOwnerAddress, err := AddressFromCompressedPublicKeyHex(normalized.SignerPublicKey)
	if err != nil {
		return types.RelayDescriptor{}, err
	}
	if normalized.OwnerAddress != derivedOwnerAddress {
		return types.RelayDescriptor{}, errors.New("owner_address does not match signer_public_key")
	}

	if normalized.SupportsOverlayPeer {
		if err := ValidateWireGuardPublicKey(normalized.WireGuardPublicKey); err != nil {
			return types.RelayDescriptor{}, err
		}
		if err := ValidateWireGuardEndpoint(normalized.WireGuardEndpoint); err != nil {
			return types.RelayDescriptor{}, err
		}
		if err := ValidateOverlayIPv4(normalized.OverlayIPv4); err != nil {
			return types.RelayDescriptor{}, err
		}
	}

	if err := VerifyDescriptor(normalized); err != nil {
		return types.RelayDescriptor{}, err
	}
	return normalized, nil
}

func ValidateWireGuardPublicKey(raw string) error {
	key := strings.TrimSpace(raw)
	if key == "" {
		return errors.New("wireguard_public_key is required")
	}
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return errors.New("wireguard_public_key must be base64 encoded")
	}
	if len(decoded) != 32 {
		return errors.New("wireguard_public_key must be 32 bytes")
	}
	return nil
}

func ValidateWireGuardEndpoint(raw string) error {
	endpoint := strings.TrimSpace(raw)
	if endpoint == "" {
		return errors.New("wireguard_endpoint is required")
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return errors.New("wireguard_endpoint must be host:port")
	}
	if strings.TrimSpace(host) == "" {
		return errors.New("wireguard_endpoint host is required")
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum <= 0 || portNum > 65535 {
		return errors.New("wireguard_endpoint port is invalid")
	}
	return nil
}

func ValidateOverlayIPv4(raw string) error {
	ipText := strings.TrimSpace(raw)
	if ipText == "" {
		return errors.New("overlay_ipv4 is required")
	}
	ip := net.ParseIP(ipText)
	if ip == nil || ip.To4() == nil {
		return errors.New("overlay_ipv4 must be a valid IPv4 address")
	}
	return nil
}

func NormalizeOverlayCIDRs(inputs []string) ([]string, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(inputs))
	out := make([]string, 0, len(inputs))
	for _, input := range inputs {
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		_, network, err := net.ParseCIDR(input)
		if err != nil {
			return nil, fmt.Errorf("invalid overlay cidr %q", input)
		}
		normalized := network.String()
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out, nil
}
