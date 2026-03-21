package types

import "time"

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
	ExpiresAt    time.Time
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	ID           string
	Name         string
	BPS          int64
	ClientIP     string
	Hostname     string
	Bootstraps   []string
	UDPEnabled   bool
	Metadata     LeaseMetadata
	OwnerAddress string
	Ready        int
	UDPPort      int
	IsApproved   bool
	IsBanned     bool
	IsDenied     bool
	IsIPBanned   bool
}
