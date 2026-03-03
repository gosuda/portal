package manager

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mustCIDR(t *testing.T, raw string) *net.IPNet {
	t.Helper()

	_, network, err := net.ParseCIDR(raw)
	if err != nil {
		t.Fatalf("parse CIDR %q: %v", raw, err)
	}
	return network
}

func TestExtractClientIPTrustsForwardedHeadersOnlyFromTrustedProxy(t *testing.T) {
	SetTrustedProxyCIDRs([]*net.IPNet{mustCIDR(t, "10.0.0.0/8")})
	t.Cleanup(func() {
		SetTrustedProxyCIDRs(nil)
	})

	trustedReq := httptest.NewRequest(http.MethodGet, "http://localhost", nil)
	trustedReq.RemoteAddr = "10.1.2.3:45000"
	trustedReq.Header.Set("X-Forwarded-For", "203.0.113.10, 10.1.2.3")

	if got := ExtractClientIP(trustedReq, true); got != "203.0.113.10" {
		t.Fatalf("expected forwarded client IP from trusted proxy, got %q", got)
	}

	untrustedReq := httptest.NewRequest(http.MethodGet, "http://localhost", nil)
	untrustedReq.RemoteAddr = "198.51.100.5:45000"
	untrustedReq.Header.Set("X-Forwarded-For", "203.0.113.10")

	if got := ExtractClientIP(untrustedReq, true); got != "198.51.100.5" {
		t.Fatalf("expected remote IP fallback for untrusted proxy, got %q", got)
	}
}

func TestExtractClientIPDoesNotTrustHeadersWithoutAllowlist(t *testing.T) {
	SetTrustedProxyCIDRs(nil)

	req := httptest.NewRequest(http.MethodGet, "http://localhost", nil)
	req.RemoteAddr = "10.1.2.3:45000"
	req.Header.Set("X-Real-IP", "203.0.113.77")

	if got := ExtractClientIP(req, true); got != "10.1.2.3" {
		t.Fatalf("expected remote IP when allowlist is empty, got %q", got)
	}
}

func TestIsTrustedProxyRemoteAddr(t *testing.T) {
	SetTrustedProxyCIDRs([]*net.IPNet{
		mustCIDR(t, "10.0.0.0/8"),
		mustCIDR(t, "2001:db8::/32"),
	})
	t.Cleanup(func() {
		SetTrustedProxyCIDRs(nil)
	})

	if !IsTrustedProxyRemoteAddr("10.9.8.7:443") {
		t.Fatal("expected IPv4 remote to match trusted CIDR")
	}
	if !IsTrustedProxyRemoteAddr("[2001:db8::1]:443") {
		t.Fatal("expected IPv6 remote to match trusted CIDR")
	}
	if IsTrustedProxyRemoteAddr("198.51.100.2:443") {
		t.Fatal("did not expect non-allowlisted remote to be trusted")
	}
}

func TestIsIPBannedByPolicy(t *testing.T) {
	ipManager := NewIPManager()
	ipManager.BanIP("203.0.113.22")

	tests := []struct {
		name      string
		manager   *IPManager
		candidate string
		want      bool
	}{
		{
			name:      "nil manager",
			manager:   nil,
			candidate: "203.0.113.22",
			want:      false,
		},
		{
			name:      "empty candidate",
			manager:   ipManager,
			candidate: "   ",
			want:      false,
		},
		{
			name:      "trimmed banned ip",
			manager:   ipManager,
			candidate: " 203.0.113.22 ",
			want:      true,
		},
		{
			name:      "not banned ip",
			manager:   ipManager,
			candidate: "203.0.113.99",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsIPBannedByPolicy(tt.manager, tt.candidate); got != tt.want {
				t.Fatalf("IsIPBannedByPolicy(%q)=%v, want %v", tt.candidate, got, tt.want)
			}
		})
	}
}
