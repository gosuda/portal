package transport

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

const (
	// OnionCellSize is the number of bytes we send as hop metadata.
	OnionCellSize   = 64
	nextHopHintSize = 32
	onionMetaSize   = 16
	defaultMaxHops  = 3
)

// Cell is a fixed-size hop metadata frame that precedes the payload stream.
type Cell struct {
	Buffer [OnionCellSize]byte
}

// ForwardingMeta keeps TTL-style hop information without exposing the full path.
type ForwardingMeta struct {
	TTL   uint8
	Flags uint8
	_     uint16 // reserved for future flags
	Nonce [12]byte
}

// OnionLayer contains one hop's metadata and verification hint.
type OnionLayer struct {
	Meta        ForwardingMeta
	NextHopHint [nextHopHintSize]byte
}

// HopCipher encrypts the small metadata header (payload remains untouched).
type HopCipher interface {
	Seal([]byte) ([]byte, error)
	Open([]byte) ([]byte, error)
}

// NewNoopCipher returns a passthrough cipher placeholder.
func NewNoopCipher() HopCipher { return noopCipher{} }

type noopCipher struct{}

func (noopCipher) Seal(b []byte) ([]byte, error) { return append([]byte(nil), b...), nil }
func (noopCipher) Open(b []byte) ([]byte, error) { return append([]byte(nil), b...), nil }

// EncodeOnionLayer packs the layer into a Cell using the cipher.
func EncodeOnionLayer(layer OnionLayer, cipher HopCipher) (Cell, error) {
	var cell Cell
	plain := make([]byte, onionMetaSize+nextHopHintSize)
	plain[0] = layer.Meta.TTL
	plain[1] = layer.Meta.Flags
	binary.BigEndian.PutUint16(plain[2:], 0)
	copy(plain[4:16], layer.Meta.Nonce[:])
	copy(plain[16:], layer.NextHopHint[:])
	if cipher == nil {
		cipher = NewNoopCipher()
	}
	sealed, err := cipher.Seal(plain)
	if err != nil {
		return cell, err
	}
	if len(sealed) > len(cell.Buffer) {
		return cell, errors.New("onion header too large")
	}
	copy(cell.Buffer[:], sealed)
	return cell, nil
}

// DecodeOnionLayer unpacks the metadata for the current hop.
func DecodeOnionLayer(cell Cell, cipher HopCipher) (OnionLayer, error) {
	var layer OnionLayer
	if cipher == nil {
		cipher = NewNoopCipher()
	}
	plain, err := cipher.Open(cell.Buffer[:])
	if err != nil {
		return layer, err
	}
	if len(plain) < onionMetaSize+nextHopHintSize {
		return layer, errors.New("onion header truncated")
	}
	layer.Meta.TTL = plain[0]
	layer.Meta.Flags = plain[1]
	copy(layer.Meta.Nonce[:], plain[4:16])
	copy(layer.NextHopHint[:], plain[16:16+nextHopHintSize])
	return layer, nil
}

// HashNodeID derives a hint for a relay ID bound to the hop nonce.
func HashNodeID(id string, nonce [12]byte) [nextHopHintSize]byte {
	payload := make([]byte, len(nonce)+len(id))
	copy(payload, nonce[:])
	copy(payload[len(nonce):], []byte(id))
	return sha256.Sum256(payload)
}

// HintMatches verifies that the hint corresponds to id with the given nonce.
func HintMatches(id string, nonce [12]byte, hint [nextHopHintSize]byte) bool {
	sum := HashNodeID(id, nonce)
	return sum == hint
}

// Advance decrements the TTL. When TTL hits zero the circuit is exhausted.
func (m ForwardingMeta) Advance() ForwardingMeta {
	if m.TTL == 0 {
		return m
	}
	m.TTL--
	return m
}

// NewMeta creates forwarding metadata with a random nonce.
func NewMeta(maxHops int) ForwardingMeta {
	meta := ForwardingMeta{}
	switch {
	case maxHops <= 0:
		meta.TTL = defaultMaxHops
	case maxHops > 255:
		meta.TTL = 255
	default:
		meta.TTL = uint8(maxHops)
	}
	_, _ = rand.Read(meta.Nonce[:])
	return meta
}
