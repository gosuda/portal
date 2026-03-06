package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gosuda.org/portal/portal"
)

type leaseRow struct {
	TTL       string `json:"ttl"`
	Metadata  string `json:"metadata"`
	Kind      string `json:"kind"`
	DNS       string `json:"dns"`
	Name      string `json:"name"`
	Peer      string `json:"peer"`
	Link      string `json:"link"`
	Hide      bool   `json:"hide"`
	Connected bool   `json:"connected"`
}

func convertLeaseEntriesToRows(serv *portal.Server, includeHidden bool, portalURL string) []leaseRow {
	if serv == nil {
		return nil
	}
	snapshots := serv.ListLeases()
	rows := make([]leaseRow, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if !includeHidden && snapshot.Metadata.Hide {
			continue
		}
		metadataJSON, _ := json.Marshal(snapshot.Metadata)
		host := ""
		if len(snapshot.Hostnames) > 0 {
			host = snapshot.Hostnames[0]
		}
		rows = append(rows, leaseRow{
			TTL:       formatDuration(time.Until(snapshot.ExpiresAt)),
			Metadata:  string(metadataJSON),
			Kind:      "https",
			DNS:       host,
			Name:      snapshot.Name,
			Peer:      snapshot.ID,
			Link:      leaseLink(host, portalURL),
			Hide:      snapshot.Metadata.Hide,
			Connected: snapshot.Ready > 0,
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

func leaseLink(host, portalURL string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	return "https://" + host + "/"
}
