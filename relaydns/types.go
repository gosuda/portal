package relaydns

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Defaults shared by server and client.
const (
	DefaultProtocol = "/dnsportal/http/1.0"
	DefaultTopic    = "dnsportal.backends"
)

type Hosts struct {
	ServerPeer  string   `json:"serverPeer"`
	ServerAddrs []string `json:"serverAddrs"`
	Peers       []string `json:"peers"` // connected peer IDs
}

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

// AdminPage is a simple view model used by the server admin UI template.
// It intentionally lives here so other binaries can share the same model if needed.
type AdminPage struct {
	NodeID string
	Addrs  []string
	Rows   []AdminRow
}

// AdminRow represents a single backend entry as shown on the admin index.
type AdminRow struct {
	Peer      string
	Name      string
	DNS       string
	LastSeen  string
	Link      string
	TTL       string
	Connected bool
	Kind      string
}
