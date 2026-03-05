package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/policy"
	"gosuda.org/portal/types"
)

const (
	leaseConnectedWindow = 15 * time.Second
	staleLeaseHideWindow = 3 * time.Minute
)

// leaseRow represents a lease entry for display in admin UI and frontend.
type leaseRow struct {
	TTL          string
	Metadata     string
	Kind         string
	IP           string
	DNS          string
	LastSeen     string
	LastSeenISO  string
	FirstSeenISO string
	Name         string
	Peer         string
	Link         string
	BPS          int64
	Hide         bool
	StaleRed     bool
	IsApproved   bool
	IsDenied     bool
	Connected    bool
	IsIPBanned   bool
}

// formatDuration formats a duration for TTL display.
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d > time.Hour {
		return fmt.Sprintf("%.0fh", d.Hours())
	}
	if d > time.Minute {
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	return fmt.Sprintf("%.0fs", d.Seconds())
}

// formatLastSeen formats a duration since last seen.
func formatLastSeen(d time.Duration) string {
	if d >= time.Hour {
		h := int(d / time.Hour)
		m := int((d % time.Hour) / time.Minute)
		if m > 0 {
			return fmt.Sprintf("%dh %dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	if d >= time.Minute {
		m := int(d / time.Minute)
		s := int((d % time.Minute) / time.Second)
		if s > 0 {
			return fmt.Sprintf("%dm %ds", m, s)
		}
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

func isLeaseConnected(since time.Duration) bool {
	return since < leaseConnectedWindow
}

// fromLeaseEntry populates the leaseRow from a LeaseEntry with common fields.
func (r *leaseRow) fromLeaseEntry(entry *types.LeaseEntry, admin *Admin, portalURL string) {
	lease := entry.Lease
	identityID := lease.ID
	since := max(time.Since(entry.LastSeen), 0)
	connected := isLeaseConnected(since)

	name := lease.Name
	if name == "" {
		name = "(unnamed)"
	}

	kind := "http"
	if lease.TLS {
		kind = "https"
	}

	dnsLabel := identityID
	if len(dnsLabel) > 8 {
		dnsLabel = dnsLabel[:8] + "..."
	}

	var bps int64
	if admin != nil {
		if bpsMgr := admin.GetBPSManager(); bpsMgr != nil {
			bps = bpsMgr.GetBPSLimit(identityID)
		}
	}

	metadata := lease.Metadata
	metadataStr := ""
	if b, err := json.Marshal(metadata); err == nil {
		metadataStr = string(b)
	} else {
		log.Warn().Err(err).Str("lease_id", identityID).Msg("[leaseRow] Failed to marshal lease metadata")
	}

	r.Peer = identityID
	r.Name = name
	r.Kind = kind
	r.Connected = connected
	r.DNS = dnsLabel
	r.LastSeen = formatLastSeen(since)
	r.LastSeenISO = entry.LastSeen.UTC().Format(time.RFC3339)
	r.FirstSeenISO = entry.FirstSeen.UTC().Format(time.RFC3339)
	r.TTL = formatDuration(time.Until(entry.Lease.Expires))
	linkLabel := identityID
	if normalized, ok := types.NormalizeServiceName(lease.Name); ok {
		linkLabel = normalized
	} else if normalized, ok := types.NormalizeServiceName(identityID); ok {
		linkLabel = normalized
	}

	publicHost := types.PortalRootHost(portalURL)
	if publicHost == "" {
		publicHost = types.PortalHostPort(portalURL)
	}
	if linkLabel != "" && publicHost != "" {
		r.Link = fmt.Sprintf("//%s.%s/", linkLabel, publicHost)
	} else {
		r.Link = ""
	}
	r.StaleRed = !connected && since >= leaseConnectedWindow
	r.Hide = metadata.Hide
	r.Metadata = metadataStr
	r.BPS = bps

	if admin != nil {
		if approveMgr := admin.GetApproveManager(); approveMgr != nil {
			r.IsApproved = approveMgr.GetApprovalMode() == policy.ModeAuto || approveMgr.IsLeaseApproved(identityID)
			r.IsDenied = approveMgr.IsLeaseDenied(identityID)
		}

		if ipMgr := admin.GetIPManager(); ipMgr != nil {
			r.IP = ipMgr.GetLeaseIP(identityID)
			if r.IP != "" {
				r.IsIPBanned = ipMgr.IsIPBanned(r.IP)
			}
		}
	}
}

// convertLeaseEntriesToRows converts LeaseEntry data to leaseRow format.
// If forAdmin is true, includes all leases with admin-only fields.
// If forAdmin is false, filters out banned, unapproved, hidden, and stale leases.
func convertLeaseEntriesToRows(serv *portal.RelayServer, admin *Admin, forAdmin bool, portalURL string) []leaseRow {
	leaseEntries := serv.GetLeaseManager().GetAllLeaseEntries()
	rows := []leaseRow{}
	now := time.Now()

	bannedList := serv.GetLeaseManager().GetBannedLeases()
	bannedMap := make(map[string]struct{}, len(bannedList))
	for _, b := range bannedList {
		bannedMap[b] = struct{}{}
	}

	for _, entry := range leaseEntries {
		if now.After(entry.Lease.Expires) {
			continue
		}

		identityID := entry.Lease.ID
		metadata := entry.Lease.Metadata

		if !forAdmin {
			if _, banned := bannedMap[identityID]; banned {
				continue
			}
			if admin != nil {
				approveManager := admin.GetApproveManager()
				if approveManager.GetApprovalMode() == policy.ModeManual && !approveManager.IsLeaseApproved(identityID) {
					continue
				}
			}
			if metadata.Hide {
				continue
			}
			since := max(now.Sub(entry.LastSeen), 0)
			connected := isLeaseConnected(since)
			if !connected && since >= staleLeaseHideWindow {
				continue
			}
		}

		var row leaseRow
		row.fromLeaseEntry(entry, admin, portalURL)
		rows = append(rows, row)
	}

	return rows
}
