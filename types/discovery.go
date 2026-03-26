package types

import "time"

type RelayDescriptor struct {
	RelayID string `json:"relay_id"`

	OwnerAddress    string `json:"owner_address"`
	SignerPublicKey string `json:"signer_public_key"`

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
	SupportsWitness     bool `json:"supports_witness,omitempty"`
	SupportsVPNExit     bool `json:"supports_vpn_exit,omitempty"`

	StatusState string `json:"status_state,omitempty"`

	Region  string `json:"region,omitempty"`
	Country string `json:"country,omitempty"`

	ReputationScore    float64 `json:"reputation_score,omitempty"`
	WitnessCount       uint64  `json:"witness_count,omitempty"`
	MITMSuspectedCount uint64  `json:"mitm_suspected_count,omitempty"`
	MITMQuarantined    bool    `json:"mitm_quarantined,omitempty"`

	LastMITMDetectedAt time.Time `json:"last_mitm_detected_at,omitempty"`

	DescriptorSignature string `json:"descriptor_signature"`
}

type PeerLifecycleState string

const (
	PeerStateKnown      PeerLifecycleState = "known"
	PeerStateVerified   PeerLifecycleState = "verified"
	PeerStateAdvertised PeerLifecycleState = "advertised"
	PeerStateExpired    PeerLifecycleState = "expired"
)

type PeerState struct {
	Descriptor          RelayDescriptor    `json:"descriptor"`
	State               PeerLifecycleState `json:"state"`
	FirstSeenAt         time.Time          `json:"first_seen_at"`
	LastSeenAt          time.Time          `json:"last_seen_at"`
	ConsecutiveFailures int                `json:"consecutive_failures,omitempty"`
}

type DesiredPeer struct {
	RelayID            string   `json:"relay_id"`
	WireGuardPublicKey string   `json:"wireguard_public_key"`
	WireGuardEndpoint  string   `json:"wireguard_endpoint"`
	AllowedIPs         []string `json:"allowed_ips,omitempty"`
}
