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
	Expires   time.Time
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

// WithDescription sets the lease description.
func WithDescription(description string) MetadataOption {
	return func(m *Metadata) {
		m.Description = description
	}
}

// WithTags sets the lease tags.
func WithTags(tags []string) MetadataOption {
	return func(m *Metadata) {
		m.Tags = tags
	}
}

// WithThumbnail sets the lease thumbnail URL.
func WithThumbnail(thumbnail string) MetadataOption {
	return func(m *Metadata) {
		m.Thumbnail = thumbnail
	}
}

// WithOwner sets the lease owner.
func WithOwner(owner string) MetadataOption {
	return func(m *Metadata) {
		m.Owner = owner
	}
}

// WithHide sets whether to hide the lease from public listings.
func WithHide(hide bool) MetadataOption {
	return func(m *Metadata) {
		m.Hide = hide
	}
}
