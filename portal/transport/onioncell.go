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

// ForwardingMeta keeps TTL-style hop information.
type ForwardingMeta struct {
	Hop     uint8
	MaxHops uint8
	_       uint16 // reserved for future flags
	Nonce   [12]byte
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
	plain[0] = layer.Meta.Hop
	plain[1] = layer.Meta.MaxHops
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
	layer.Meta.Hop = plain[0]
	layer.Meta.MaxHops = plain[1]
	copy(layer.Meta.Nonce[:], plain[4:16])
	copy(layer.NextHopHint[:], plain[16:16+nextHopHintSize])
	return layer, nil
}

// HashNodeID derives a stable hint for a relay ID.
func HashNodeID(id string) [nextHopHintSize]byte {
	return sha256.Sum256([]byte(id))
}

// HintMatches verifies that the hint corresponds to id.
func HintMatches(id string, hint [nextHopHintSize]byte) bool {
	sum := HashNodeID(id)
	return sum == hint
}

// Advance increments the hop counter, enforcing MaxHops.
func (m ForwardingMeta) Advance() ForwardingMeta {
	if m.MaxHops == 0 {
		m.MaxHops = defaultMaxHops
	}
	if m.Hop < m.MaxHops {
		m.Hop++
	}
	return m
}

// NewMeta creates forwarding metadata with a random nonce.
func NewMeta(maxHops int) ForwardingMeta {
	meta := ForwardingMeta{MaxHops: uint8(maxHops)}
	if meta.MaxHops == 0 {
		meta.MaxHops = defaultMaxHops
	}
	_, _ = rand.Read(meta.Nonce[:])
	return meta
}
