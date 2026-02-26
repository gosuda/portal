package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
)

// parseURLs splits a comma-separated string into a list of trimmed, non-empty URLs.
func parseURLs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// isSubdomain reports whether host matches the given domain pattern.
func isSubdomain(domain, host string) bool {
	if host == "" || domain == "" {
		return false
	}

	h := strings.ToLower(stripPort(stripScheme(host)))
	d := strings.ToLower(stripPort(stripScheme(domain)))

	if strings.HasPrefix(d, "*.") {
		suffix := d[1:]
		return len(h) > len(suffix) && strings.HasSuffix(h, suffix)
	}

	if h == d {
		return true
	}

	return strings.HasSuffix(h, "."+d)
}

func stripScheme(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	return s
}

func stripWildCard(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "*.")
	return s
}

func stripPort(s string) string {
	if s == "" {
		return s
	}
	if idx := strings.LastIndexByte(s, ':'); idx >= 0 && idx+1 < len(s) {
		port := s[idx+1:]
		digits := true
		for _, ch := range port {
			if ch < '0' || ch > '9' {
				digits = false
				break
			}
		}
		if digits {
			return s[:idx]
		}
	}
	return s
}

// defaultAppPattern builds a wildcard subdomain pattern from a base portal URL or host.
func defaultAppPattern(base string) string {
	base = strings.TrimSpace(strings.TrimSuffix(base, "/"))
	if base == "" {
		return "*.localhost:4017"
	}
	host := stripWildCard(stripScheme(base))
	if host == "" {
		return "*.localhost:4017"
	}
	if strings.HasPrefix(host, "*.") {
		return host
	}
	return "*." + host
}

// defaultBootstrapFrom derives a relay API bootstrap URL from a base portal URL or host.
func defaultBootstrapFrom(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return "http://localhost:4017"
	}

	if !strings.Contains(base, "://") {
		base = "http://" + base
	}

	u, err := url.Parse(strings.TrimSuffix(base, "/"))
	if err != nil || u.Host == "" {
		return "http://localhost:4017"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "http://localhost:4017"
	}
	if p := strings.TrimSpace(u.Path); p != "" && p != "/" {
		return "http://localhost:4017"
	}

	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimSuffix(u.String(), "/")
}

// portalHostPort returns normalized host[:port] from a portal URL-like input.
func portalHostPort(portalURL string) string {
	return strings.ToLower(strings.TrimSpace(
		stripWildCard(stripScheme(portalURL)),
	))
}

// portalBaseHostNoPort returns host without port from a portal URL-like input.
func portalBaseHostNoPort(portalURL string) string {
	return strings.ToLower(strings.TrimSpace(stripPort(portalHostPort(portalURL))))
}

// servicePublicURL returns a service URL derived from portalURL and service name.
func servicePublicURL(portalURL, serviceName string) string {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" {
		return ""
	}

	raw := strings.TrimSpace(portalURL)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil || strings.TrimSpace(u.Host) == "" {
		return ""
	}

	host := strings.TrimSpace(stripWildCard(u.Host))
	if host == "" {
		return ""
	}

	scheme := strings.TrimSpace(u.Scheme)
	if scheme == "" {
		scheme = "http"
	}

	return fmt.Sprintf("%s://%s.%s", scheme, serviceName, host)
}

// getContentType returns the MIME type for a file extension
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

// setCORSHeaders sets permissive CORS headers for GET/OPTIONS and common headers
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Accept-Encoding")
}

// extractBaseDomain extracts the base domain from a URL.
// For example, "https://app.portal.com" -> "portal.com"
func extractBaseDomain(portalURL string) string {
	portalURL = strings.TrimSpace(portalURL)
	if portalURL == "" {
		return ""
	}

	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(strings.ToLower(portalURL), prefix) {
			portalURL = portalURL[len(prefix):]
			break
		}
	}

	if idx := strings.Index(portalURL, ":"); idx > 0 {
		portalURL = portalURL[:idx]
	}

	if idx := strings.Index(portalURL, "/"); idx > 0 {
		portalURL = portalURL[:idx]
	}

	portalURL = strings.TrimPrefix(portalURL, "*.")

	parts := strings.Split(portalURL, ".")
	if len(parts) < 2 {
		return ""
	}

	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}

// leaseNameFromHost extracts the lease name from a subdomain host.
func leaseNameFromHost(host, appURL string) (string, bool) {
	if !isSubdomain(appURL, host) {
		return "", false
	}

	normalizedHost := strings.ToLower(strings.TrimSpace(stripPort(host)))
	baseHost := strings.ToLower(strings.TrimSpace(
		stripPort(stripWildCard(stripScheme(appURL))),
	))

	if normalizedHost == "" || baseHost == "" || normalizedHost == baseHost {
		return "", false
	}

	suffix := "." + baseHost
	if !strings.HasSuffix(normalizedHost, suffix) {
		return "", false
	}

	leaseName := strings.TrimSuffix(normalizedHost, suffix)
	if leaseName == "" || strings.Contains(leaseName, ".") {
		return "", false
	}

	return leaseName, true
}

// openLeaseConnection acquires a reverse connection for the given lease ID.
func openLeaseConnection(leaseID string, serv *portal.RelayServer) (net.Conn, func(), error) {
	reverseConn, err := serv.GetReverseHub().AcquireStarted(leaseID, portal.ReverseHTTPWait)
	if err != nil {
		return nil, nil, fmt.Errorf("no reverse connection available for lease %s: %w", leaseID, err)
	}
	return reverseConn.Conn, reverseConn.Close, nil
}

// withCORSMiddleware wraps a handler with CORS headers.
func withCORSMiddleware(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		h(w, r)
	}
}

// leaseRow represents a lease entry for display in admin UI and frontend.
type leaseRow struct {
	Peer         string
	Name         string
	Kind         string
	Connected    bool
	DNS          string
	LastSeen     string
	LastSeenISO  string
	FirstSeenISO string
	TTL          string
	Link         string
	StaleRed     bool
	Hide         bool
	Metadata     string
	BPS          int64  // bytes-per-second limit (0 = unlimited)
	IsApproved   bool   // whether lease is approved (for manual mode)
	IsDenied     bool   // whether lease is denied (for manual mode)
	IP           string // client IP address (for IP-based ban)
	IsIPBanned   bool   // whether the IP is banned
}

// formatDuration formats a duration for TTL display.
func (leaseRow) formatDuration(d time.Duration) string {
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
func (leaseRow) formatLastSeen(d time.Duration) string {
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

// isConnected returns true if the lease was seen recently.
func (leaseRow) isConnected(since time.Duration) bool {
	return since < 15*time.Second
}

// fromLeaseEntry populates the leaseRow from a LeaseEntry with common fields.
func (r *leaseRow) fromLeaseEntry(entry *portal.LeaseEntry, admin *Admin, portalURL string) {
	lease := entry.Lease
	identityID := lease.ID
	since := max(time.Since(entry.LastSeen), 0)
	connected := r.isConnected(since)

	name := lease.Name
	if name == "" {
		name = "(unnamed)"
	}

	kind := "http"
	if lease.TLSEnabled {
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
	r.LastSeen = r.formatLastSeen(since)
	r.LastSeenISO = entry.LastSeen.UTC().Format(time.RFC3339)
	r.FirstSeenISO = entry.FirstSeen.UTC().Format(time.RFC3339)
	r.TTL = r.formatDuration(time.Until(entry.Expires))
	r.Link = fmt.Sprintf("//%s.%s/", lease.Name, portalHostPort(portalURL))
	r.StaleRed = !connected && since >= 15*time.Second
	r.Hide = entry.ParsedMetadata != nil && entry.ParsedMetadata.Hide
	r.Metadata = metadataStr
	r.BPS = bps

	if admin != nil {
		r.IsApproved = admin.approveManager.GetApprovalMode() == manager.ApprovalModeAuto || admin.approveManager.IsLeaseApproved(identityID)
		r.IsDenied = admin.approveManager.IsLeaseDenied(identityID)

		if admin.ipManager != nil {
			r.IP = admin.ipManager.GetLeaseIP(identityID)
			if r.IP != "" {
				r.IsIPBanned = admin.ipManager.IsIPBanned(r.IP)
			}
		}
	}
}

// convertLeaseEntriesToRows converts LeaseEntry data to leaseRow format.
// If forAdmin is true, includes all leases with admin-only fields.
// If forAdmin is false, filters out banned, unapproved, hidden, and stale leases.
func convertLeaseEntriesToRows(serv *portal.RelayServer, admin *Admin, forAdmin bool) []leaseRow {
	leaseEntries := serv.GetLeaseManager().GetAllLeaseEntries()
	rows := []leaseRow{}
	now := time.Now()

	bannedList := serv.GetLeaseManager().GetBannedLeases()
	bannedMap := make(map[string]struct{}, len(bannedList))
	for _, b := range bannedList {
		bannedMap[string(b)] = struct{}{}
	}

	for _, entry := range leaseEntries {
		if now.After(entry.Expires) {
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
				if approveManager.GetApprovalMode() == manager.ApprovalModeManual && !approveManager.IsLeaseApproved(identityID) {
					continue
				}
			}
			if metadata.Hide {
				continue
			}
			since := max(now.Sub(entry.LastSeen), 0)
			connected := (&leaseRow{}).isConnected(since)
			if !connected && since >= 3*time.Minute {
				continue
			}
		}

		var row leaseRow
		row.fromLeaseEntry(entry, admin, flagPortalURL)
		rows = append(rows, row)
	}

	return rows
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error().Err(err).Msg("[HTTP] Failed to encode response")
	}
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
