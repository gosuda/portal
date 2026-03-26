package utils

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/idna"
)

// Input parsing and normalization.
func SplitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func ParseCIDRs(raw string) ([]*net.IPNet, error) {
	parts := SplitCSV(raw)
	if len(parts) == 0 {
		return nil, nil
	}

	cidrs := make([]*net.IPNet, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		_, network, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("invalid cidr %q: %w", part, err)
		}
		key := network.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		cidrs = append(cidrs, network)
	}
	return cidrs, nil
}

func NormalizeDNSLabel(raw string) (string, error) {
	label := sanitizeDNSLabelInput(raw)
	if label == "" {
		return "", errors.New("name is required")
	}

	if !isPlainDNSLabel(label) {
		ascii, err := idna.Lookup.ToASCII(label)
		if err != nil {
			return "", errors.New("name is invalid")
		}
		label = NormalizeHostname(ascii)
	}
	if strings.Contains(label, ".") {
		return "", errors.New("name must be a single dns label")
	}
	if len(label) > 63 {
		return "", errors.New("name must be 63 characters or fewer")
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return "", errors.New("name must not start or end with hyphen")
	}
	for _, r := range label {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return "", errors.New("name must contain only letters, numbers, or hyphen")
	}
	return label, nil
}

func sanitizeDNSLabelInput(raw string) string {
	input := strings.TrimSpace(strings.ToLower(raw))
	if input == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(input))
	previousHyphen := false

	for _, r := range input {
		if r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			previousHyphen = false
			continue
		}
		if previousHyphen {
			continue
		}
		b.WriteByte('-')
		previousHyphen = true
	}

	return strings.Trim(b.String(), "-")
}

func isPlainDNSLabel(label string) bool {
	for _, r := range label {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func NormalizeRelayURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("relay url is empty")
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + strings.TrimPrefix(trimmed, "//")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse relay url %q: %w", raw, err)
	}
	if parsed.Host == "" && parsed.Path != "" && !strings.Contains(parsed.Path, "/") {
		parsed, err = url.Parse("https://" + strings.TrimSpace(parsed.Path))
		if err != nil {
			return "", fmt.Errorf("parse relay url %q: %w", raw, err)
		}
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("relay url host is empty: %q", raw)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("relay url must use https: %q", raw)
	}

	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(strings.ToLower(parsed.Path), "/relay") {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/relay")
	}
	return parsed.String(), nil
}

func PortalRootHost(portalURL string) string {
	u, err := url.Parse(strings.TrimSpace(portalURL))
	if err != nil || u.Host == "" {
		return ""
	}
	return NormalizeHostname(u.Hostname())
}

func NormalizeHostname(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	return host
}

// NormalizeURLPath canonicalizes URL paths to a rooted, slash-trimmed form.
func NormalizeURLPath(raw string) string {
	clean := path.Clean(strings.TrimSpace(raw))
	if clean == "." || clean == "" {
		return "/"
	}
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	// Prevent scheme-relative or otherwise ambiguous paths like "//example" or "/\example".
	if len(clean) > 1 && (clean[1] == '/' || clean[1] == '\\') {
		clean = "/"
	}
	if clean != "/" {
		clean = strings.TrimSuffix(clean, "/")
	}
	return clean
}

func NormalizeRelayURLs(inputs ...string) ([]string, error) {
	out := make([]string, 0, len(inputs))

	for _, input := range inputs {
		for _, part := range SplitCSV(input) {
			normalized, err := NormalizeRelayURL(part)
			if err != nil {
				return nil, err
			}
			out = append(out, normalized)
		}
	}

	return uniqueURLs(out), nil
}

func FilterRelayURLs(inputs, excluded []string) []string {
	if len(inputs) == 0 {
		return nil
	}
	if len(excluded) == 0 {
		return append([]string(nil), inputs...)
	}

	skip := make(map[string]struct{}, len(excluded))
	for _, input := range excluded {
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		skip[input] = struct{}{}
	}

	filtered := make([]string, 0, len(inputs))
	for _, input := range inputs {
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		if _, ok := skip[input]; ok {
			continue
		}
		filtered = append(filtered, input)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func RemoveRelayURL(inputs []string, target string) []string {
	if len(inputs) == 0 {
		return nil
	}

	target = strings.TrimSpace(target)
	if target == "" {
		return append([]string(nil), inputs...)
	}

	filtered := make([]string, 0, len(inputs))
	for _, input := range inputs {
		input = strings.TrimSpace(input)
		if input == "" || input == target {
			continue
		}
		filtered = append(filtered, input)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func AppendUniqueRelayURL(inputs []string, target string) []string {
	target = strings.TrimSpace(target)
	if target == "" {
		return append([]string(nil), inputs...)
	}

	for _, input := range inputs {
		if strings.TrimSpace(input) == target {
			return append([]string(nil), inputs...)
		}
	}
	return append(append([]string(nil), inputs...), target)
}

func MergeRelayURLs(current, excluded, inputs []string) ([]string, error) {
	merged, err := NormalizeRelayURLs(append(append([]string(nil), current...), inputs...)...)
	if err != nil {
		return nil, err
	}
	if len(excluded) == 0 {
		return merged, nil
	}

	excluded, err = NormalizeRelayURLs(excluded...)
	if err != nil {
		return nil, err
	}

	return FilterRelayURLs(merged, excluded), nil
}

func ExcludeLocalRelayURLs(inputs ...string) ([]string, error) {
	normalized, err := NormalizeRelayURLs(inputs...)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return nil, nil
	}

	filtered := normalized[:0]
	for _, input := range normalized {
		parsed, err := url.Parse(input)
		if err != nil {
			return nil, fmt.Errorf("parse relay url %q: %w", input, err)
		}
		if IsLocalRelayHost(parsed.Hostname()) {
			continue
		}
		filtered = append(filtered, input)
	}
	if len(filtered) == 0 {
		return nil, nil
	}
	return filtered, nil
}

func uniqueURLs(inputs []string) []string {
	if len(inputs) == 0 {
		return nil
	}

	out := make([]string, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		if _, ok := seen[input]; ok {
			continue
		}
		seen[input] = struct{}{}
		out = append(out, input)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func LeaseHostname(name, rootHost string) (string, error) {
	label, err := NormalizeDNSLabel(name)
	if err != nil {
		return "", err
	}
	rootHost = NormalizeHostname(rootHost)
	if rootHost == "" {
		return "", errors.New("root host is required")
	}
	return label + "." + rootHost, nil
}

func FormatDuration(d time.Duration) string {
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

func FormatLastSeen(d time.Duration) string {
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

func DecodeBase64URLString(encoded string) (string, error) {
	decoded, err := base64.URLEncoding.DecodeString(encoded)
	if err == nil {
		return string(decoded), nil
	}

	decoded, err = base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// Network and transport helpers.
func NormalizeTargetAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("target address is required")
	}

	if strings.Contains(raw, "://") {
		targetURL, err := url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("parse target url: %w", err)
		}
		if !strings.EqualFold(targetURL.Scheme, "http") && !strings.EqualFold(targetURL.Scheme, "https") {
			return "", fmt.Errorf("unsupported target url scheme %q", targetURL.Scheme)
		}
		if targetURL.Host == "" {
			return "", errors.New("target url host is empty")
		}
		if targetURL.Path != "" && targetURL.Path != "/" {
			return "", errors.New("target url path is not supported")
		}
		if targetURL.RawQuery != "" {
			return "", errors.New("target url query is not supported")
		}
		if targetURL.Fragment != "" {
			return "", errors.New("target url fragment is not supported")
		}
		raw = targetURL.Host
	}

	if _, _, err := net.SplitHostPort(raw); err == nil {
		return raw, nil
	}
	if strings.Count(raw, ":") == 0 {
		return net.JoinHostPort(raw, "80"), nil
	}
	if ip := net.ParseIP(raw); ip != nil {
		return net.JoinHostPort(raw, "80"), nil
	}
	return "", fmt.Errorf("invalid target address %q", raw)
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

func EnsurePort(host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, "443")
}

func IsLocalRelayHost(host string) bool {
	host = NormalizeHostname(host)
	switch host {
	case "", "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.HasSuffix(host, ".localhost")
}

func AddrString(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

func RandomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Security and TLS helpers.
func TokenMatches(expected, actual string) bool {
	if len(expected) == 0 || len(actual) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func CertPoolFromPEM(rootCAPEM []byte) (*x509.CertPool, error) {
	if len(rootCAPEM) == 0 {
		return nil, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(rootCAPEM) {
		return nil, errors.New("failed to parse relay root ca")
	}
	return pool, nil
}

func SleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Random value helpers.
func RandomID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(buf)
}

func NormalizeIPPrefixes(inputs []string) []string {
	if len(inputs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(inputs))
	out := make([]string, 0, len(inputs))
	for _, input := range inputs {
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(input)
		if err != nil {
			continue
		}
		normalized := prefix.String()
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}
