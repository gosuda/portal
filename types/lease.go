package types

import "time"

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
