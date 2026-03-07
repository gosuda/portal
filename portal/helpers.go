package portal

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/gosuda/portal/v2/types"
)

const (
	defaultLeaseTTL          = 2 * time.Minute
	defaultClaimTimeout      = 10 * time.Second
	defaultIdleKeepalive     = 15 * time.Second
	defaultReadyQueueLimit   = 8
	defaultClientHelloWait   = 2 * time.Second
	defaultControlBodyLimit  = 4 << 20
	defaultSessionWriteLimit = 5 * time.Second
)

func PortalRootHost(portalURL string) string {
	u, err := url.Parse(strings.TrimSpace(portalURL))
	if err != nil || u.Host == "" {
		return ""
	}
	return normalizeHostname(u.Hostname())
}

func NormalizeRelayURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse relay url: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("relay url must use https: %q", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("relay url host is empty: %q", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func normalizeHostname(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	return host
}

func sanitizeLabel(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastHyphen := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastHyphen = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if b.Len() == 0 || lastHyphen {
				continue
			}
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "app"
	}
	return s
}

func suggestHostname(name, rootHost string) string {
	label := sanitizeLabel(name)
	if rootHost == "" {
		return label
	}
	return label + "." + rootHost
}

func randomID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(buf)
}

func durationOrDefault(v, fallback time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return fallback
}

func intOrDefault(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

func normalizeMetadata(meta types.LeaseMetadata) types.LeaseMetadata {
	meta.Description = strings.TrimSpace(meta.Description)
	meta.Owner = strings.TrimSpace(meta.Owner)
	meta.Thumbnail = strings.TrimSpace(meta.Thumbnail)
	meta.Tags = normalizeTags(meta.Tags)
	return meta
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func HostPortOrLoopback(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
