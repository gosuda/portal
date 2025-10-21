package relaydns

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

type Advertise struct {
	Peer  string    `json:"peer"`
	Name  string    `json:"name,omitempty"`
	DNS   string    `json:"dns,omitempty"`
	Addrs []string  `json:"addrs"`
	Ready bool      `json:"ready"`
	Load  float64   `json:"load"`
	TS    time.Time `json:"ts"`
	TTL   int       `json:"ttl,omitempty"` // seconds
	Proto string    `json:"proto,omitempty"`
}

type HostEntry struct {
	Info      Advertise
	AddrInfo  *peer.AddrInfo
	LastSeen  time.Time
	Connected bool
}

// Removed Picker: selection is explicit via /peer/{peerID}/...

func protocolID(s string) protocol.ID {
	return protocol.ID(s)
}
