package types

import "time"

const (
	// Reverse-connect protocol markers and headers.
	ReverseKeepaliveMarker    = byte(0x00)
	NonTLSStartMarker         = byte(0x01)
	TLSStartMarker            = byte(0x02)
	ReverseConnectTokenHeader = "X-Portal-Reverse-Token"
)

// Lease represents a registered service.
type Lease struct {
	Expires      time.Time `json:"expires"`
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	ReverseToken string    `json:"-"`
	Metadata     Metadata  `json:"metadata"`
	TLS          bool      `json:"tls"`
}

// LeaseEntry represents a registered lease with expiration tracking.
type LeaseEntry struct {
	Lease     *Lease
	LastSeen  time.Time
	FirstSeen time.Time
}

// Metadata holds service metadata for a lease.
type Metadata struct {
	Description string   `json:"description,omitempty"`
	Thumbnail   string   `json:"thumbnail,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Hide        bool     `json:"hide,omitempty"`
}

// MetadataOption configures Metadata.
type MetadataOption func(*Metadata)

func WithDescription(description string) MetadataOption {
	return func(m *Metadata) {
		m.Description = description
	}
}

func WithTags(tags []string) MetadataOption {
	return func(m *Metadata) {
		m.Tags = tags
	}
}

func WithThumbnail(thumbnail string) MetadataOption {
	return func(m *Metadata) {
		m.Thumbnail = thumbnail
	}
}

func WithOwner(owner string) MetadataOption {
	return func(m *Metadata) {
		m.Owner = owner
	}
}

func WithHide(hide bool) MetadataOption {
	return func(m *Metadata) {
		m.Hide = hide
	}
}
