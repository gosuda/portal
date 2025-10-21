package relaydns

import (
    "time"

    "github.com/libp2p/go-libp2p/core/peer"
)

// Defaults shared by server and client.
const (
    DefaultProtocol = "/relaydns/http/1.0"
    DefaultTopic    = "relaydns.backends"
)

type Advertise struct {
    Peer  string    `json:"peer"`
    Name  string    `json:"name,omitempty"`
    DNS   string    `json:"dns,omitempty"`
	Addrs []string  `json:"addrs"`
	Ready bool      `json:"ready"`
	Load  float64   `json:"load"`
	TS    time.Time `json:"ts"`
	TTL   int       `json:"ttl,omitempty"`
	Proto string    `json:"proto,omitempty"`
}

type HostEntry struct {
	Info      Advertise
	AddrInfo  *peer.AddrInfo
	LastSeen  time.Time
	Connected bool
}
