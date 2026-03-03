// Package types defines all API request/response types and path constants
// for the Portal relay server and SDK.
package types

// Metadata holds service metadata for a lease.
type Metadata struct {
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Thumbnail   string   `json:"thumbnail,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	Hide        bool     `json:"hide,omitempty"`
}

// ParsedMetadata holds struct-parsed metadata for better access.
type ParsedMetadata struct {
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Thumbnail   string   `json:"thumbnail"`
	Owner       string   `json:"owner"`
	Hide        bool     `json:"hide"`
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
