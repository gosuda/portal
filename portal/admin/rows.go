package admin

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gosuda/portal/v2/portal"
)

const staleLeaseHideWindow = 3 * time.Minute

type LeaseRow struct {
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

func BuildLeaseRows(serv *portal.Server, includeAdmin bool, portalURL string) []LeaseRow {
	if serv == nil {
		return nil
	}

	now := time.Now()
	snapshots := serv.ListLeases()
	rows := make([]LeaseRow, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if now.After(snapshot.ExpiresAt) {
			continue
		}

		since := time.Duration(0)
		if !snapshot.LastSeenAt.IsZero() {
			since = max(now.Sub(snapshot.LastSeenAt), 0)
		}
		connected := snapshot.Ready > 0
		if !includeAdmin {
			if snapshot.IsBanned || snapshot.IsDenied || !snapshot.IsApproved || snapshot.Metadata.Hide {
				continue
			}
			if !connected && since >= staleLeaseHideWindow {
				continue
			}
		}

		metadataJSON, _ := json.Marshal(snapshot.Metadata)
		host := ""
		if len(snapshot.Hostnames) > 0 {
			host = snapshot.Hostnames[0]
		}

		rows = append(rows, LeaseRow{
			TTL:          formatDuration(time.Until(snapshot.ExpiresAt)),
			Metadata:     string(metadataJSON),
			Kind:         "https",
			IP:           snapshot.ClientIP,
			DNS:          host,
			LastSeen:     formatLastSeen(since),
			LastSeenISO:  formatISOTime(snapshot.LastSeenAt),
			FirstSeenISO: formatISOTime(snapshot.FirstSeenAt),
			Name:         strings.TrimSpace(snapshot.Name),
			Peer:         snapshot.ID,
			Link:         leaseLink(host),
			BPS:          0,
			Hide:         snapshot.Metadata.Hide,
			StaleRed:     !connected && since >= staleLeaseHideWindow,
			IsApproved:   snapshot.IsApproved,
			IsDenied:     snapshot.IsDenied,
			Connected:    connected,
			IsIPBanned:   snapshot.IsIPBanned,
		})
	}
	return rows
}

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

func formatLastSeen(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d >= time.Hour {
		hours := int(d / time.Hour)
		minutes := int((d % time.Hour) / time.Minute)
		if minutes > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	}
	if d >= time.Minute {
		minutes := int(d / time.Minute)
		seconds := int((d % time.Minute) / time.Second)
		if seconds > 0 {
			return fmt.Sprintf("%dm %ds", minutes, seconds)
		}
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

func formatISOTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}

func leaseLink(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	return "https://" + host + "/"
}
