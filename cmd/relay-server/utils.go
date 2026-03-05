package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/keyless"
	"gosuda.org/portal/portal/policy"
	"gosuda.org/portal/types"
)

const (
	leaseConnectedWindow = 15 * time.Second
	staleLeaseHideWindow = 3 * time.Minute
)

func isSecureRequestWithPolicy(r *http.Request, trustProxyHeaders bool) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	if !trustProxyHeaders || !policy.IsTrustedProxyRemoteAddr(r.RemoteAddr) {
		return false
	}
	if hasForwardedToken(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return hasForwardedToken(r.Header.Get("X-Forwarded-Ssl"), "on")
}

func hasForwardedToken(raw, target string) bool {
	for token := range strings.SplitSeq(raw, ",") {
		if strings.EqualFold(strings.TrimSpace(token), target) {
			return true
		}
	}
	return false
}

func isWebSocketUpgrade(req *http.Request) bool {
	if req == nil {
		return false
	}
	return hasForwardedToken(req.Header.Get("Upgrade"), "websocket")
}

// getContentType returns the MIME type for a file extension.
func getContentType(ext string) string {
	switch ext {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".wasm":
		return "application/wasm"
	case ".css":
		return "text/css"
	case ".mp4":
		return "video/mp4"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	default:
		return ""
	}
}

// setCORSHeaders sets permissive CORS headers for GET/OPTIONS and common headers.
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Accept-Encoding")
}

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

func writeAPIData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(types.APIEnvelope{
		OK:   true,
		Data: data,
	}); err != nil {
		log.Error().Err(err).Msg("[HTTP] Failed to encode API success response")
	}
}

func writeAPIOK(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(types.APIEnvelope{OK: true}); err != nil {
		log.Error().Err(err).Msg("[HTTP] Failed to encode API success response")
	}
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeAPIErrorWithData(w, status, code, message, nil)
}

func writeAPIErrorWithData(w http.ResponseWriter, status int, code, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(types.APIEnvelope{
		OK:   false,
		Data: data,
		Error: &types.APIError{
			Code:    code,
			Message: message,
		},
	}); err != nil {
		log.Error().Err(err).Msg("[HTTP] Failed to encode API error response")
	}
}

func writeSignError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(keyless.ErrorResponse{Error: message})
}

func decodeLeaseID(encoded string) (string, bool) {
	idBytes, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		idBytes, err = base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return "", false
		}
	}
	return string(idBytes), true
}
