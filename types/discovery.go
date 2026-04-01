package types

import "time"

const DiscoveryPollInterval = 1 * time.Minute

type RelayDescriptor struct {
	RelayID string `json:"relay_id"`

	Sequence  uint64    `json:"sequence"`
	Version   uint32    `json:"version"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`

	APIHTTPSAddr   string `json:"api_https_addr"`
	IngressTLSAddr string `json:"ingress_tls_addr,omitempty"`

	WireGuardPublicKey string   `json:"wireguard_public_key,omitempty"`
	WireGuardEndpoint  string   `json:"wireguard_endpoint,omitempty"`
	OverlayIPv4        string   `json:"overlay_ipv4,omitempty"`
	OverlayCIDRs       []string `json:"overlay_cidrs,omitempty"`

	SupportsTCP         bool `json:"supports_tcp,omitempty"`
	SupportsUDP         bool `json:"supports_udp,omitempty"`
	SupportsOverlayPeer bool `json:"supports_overlay_peer,omitempty"`

	Load float64 `json:"load,omitempty"`
	}
type RelayState struct {
	Descriptor          RelayDescriptor `json:"descriptor"`
	Bootstrap           bool            `json:"bootstrap,omitempty"`
	Advertised          bool            `json:"advertised,omitempty"`
	Expired             bool            `json:"expired,omitempty"`
	FirstSeenAt         time.Time       `json:"first_seen_at"`
	LastSeenAt          time.Time       `json:"last_seen_at"`
	ConsecutiveFailures int             `json:"consecutive_failures,omitempty"`
}

type DesiredPeer struct {
	RelayID            string   `json:"relay_id"`
	WireGuardPublicKey string   `json:"wireguard_public_key"`
	WireGuardEndpoint  string   `json:"wireguard_endpoint"`
	AllowedIPs         []string `json:"allowed_ips,omitempty"`
}
