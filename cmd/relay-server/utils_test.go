package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"gosuda.org/portal/cmd/relay-server/manager"
)

func mustParseCIDR(t *testing.T, raw string) *net.IPNet {
	t.Helper()

	_, network, err := net.ParseCIDR(raw)
	if err != nil {
		t.Fatalf("parse CIDR %q: %v", raw, err)
	}
	return network
}

func TestIsSecureRequest(t *testing.T) {
	originalTrustProxyHeaders := flagTrustProxyHeaders
	t.Cleanup(func() {
		flagTrustProxyHeaders = originalTrustProxyHeaders
		manager.SetTrustedProxyCIDRs(nil)
	})

	cases := []struct {
		headers   map[string]string
		name      string
		remote    string
		allowlist []*net.IPNet
		tls       bool
		trust     bool
		expected  bool
	}{
		{
			name:     "tls request is always secure",
			tls:      true,
			trust:    false,
			remote:   "198.51.100.10:443",
			expected: true,
		},
		{
			name:      "proxy headers ignored when trust flag disabled",
			trust:     false,
			remote:    "10.1.2.3:8080",
			headers:   map[string]string{"X-Forwarded-Proto": "https"},
			allowlist: []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")},
			expected:  false,
		},
		{
			name:     "proxy headers ignored with empty allowlist",
			trust:    true,
			remote:   "10.1.2.3:8080",
			headers:  map[string]string{"X-Forwarded-Proto": "https"},
			expected: false,
		},
		{
			name:      "trusted proxy with forwarded proto is secure",
			trust:     true,
			remote:    "10.1.2.3:8080",
			headers:   map[string]string{"X-Forwarded-Proto": "https"},
			allowlist: []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")},
			expected:  true,
		},
		{
			name:      "trusted proxy with forwarded ssl on is secure",
			trust:     true,
			remote:    "10.1.2.3:8080",
			headers:   map[string]string{"X-Forwarded-Ssl": "on"},
			allowlist: []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")},
			expected:  true,
		},
		{
			name:      "untrusted proxy headers are rejected",
			trust:     true,
			remote:    "198.51.100.99:8080",
			headers:   map[string]string{"X-Forwarded-Proto": "https"},
			allowlist: []*net.IPNet{mustParseCIDR(t, "10.0.0.0/8")},
			expected:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flagTrustProxyHeaders = tc.trust
			manager.SetTrustedProxyCIDRs(tc.allowlist)

			req := httptest.NewRequest(http.MethodGet, "http://localhost/admin", http.NoBody)
			req.RemoteAddr = tc.remote
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			for key, value := range tc.headers {
				req.Header.Set(key, value)
			}

			if got := isSecureRequest(req); got != tc.expected {
				t.Fatalf("isSecureRequest() = %v, want %v", got, tc.expected)
			}
		})
	}
}
