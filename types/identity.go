package types

import (
	"strings"
	"time"
)

type Identity struct {
	Name       string `json:"name,omitempty"`
	Address    string `json:"address,omitempty"`
	PublicKey  string `json:"-"`
	PrivateKey string `json:"-"`
}

func (i Identity) Copy() Identity {
	return Identity{
		Name:       i.Name,
		Address:    i.Address,
		PublicKey:  i.PublicKey,
		PrivateKey: i.PrivateKey,
	}
}

const IdentityKeySeparator = ":"

func (i Identity) Key() string {
	name := strings.TrimSpace(strings.ToLower(i.Name))
	address := strings.TrimSpace(strings.ToLower(i.Address))
	if name == "" && address == "" {
		return ""
	}
	return name + IdentityKeySeparator + address
}

type LeaseMetadata struct {
	Description string   `json:"description,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	Thumbnail   string   `json:"thumbnail,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Hide        bool     `json:"hide,omitempty"`
}

func (m LeaseMetadata) Copy() LeaseMetadata {
	return LeaseMetadata{
		Description: m.Description,
		Owner:       m.Owner,
		Thumbnail:   m.Thumbnail,
		Tags:        append([]string(nil), m.Tags...),
		Hide:        m.Hide,
	}
}

type Lease struct {
	Name        string `json:"name,omitempty"`
	ExpiresAt   time.Time
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	Hostname    string
	UDPEnabled  bool
	Metadata    LeaseMetadata
	Ready       int
}

type AdminLease struct {
	Lease
	Address    string `json:"address,omitempty"`
	BPS        int64
	ClientIP   string
	ReportedIP string
	IsApproved bool
	IsBanned   bool
	IsDenied   bool
	IsIPBanned bool
}

type RelayDescriptor struct {
	Identity

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
}

const DiscoveryPollInterval = 1 * time.Minute

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
	WireGuardPublicKey string   `json:"wireguard_public_key"`
	WireGuardEndpoint  string   `json:"wireguard_endpoint"`
	AllowedIPs         []string `json:"allowed_ips,omitempty"`
}

type DNSSECStatus struct {
	State    string `json:"state,omitempty"`
	DSRecord string `json:"ds_record,omitempty"`
	Message  string `json:"message,omitempty"`
}
