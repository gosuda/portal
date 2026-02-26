package utils

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetContentType(t *testing.T) {
	cases := map[string]string{
		".html": "text/html; charset=utf-8",
		".js":   "application/javascript",
		".json": "application/json",
		".wasm": "application/wasm",
		".css":  "text/css",
		".mp4":  "video/mp4",
		".svg":  "image/svg+xml",
		".png":  "image/png",
		".ico":  "image/x-icon",
		".bin":  "",
		"":      "",
	}
	for ext, want := range cases {
		got := GetContentType(ext)
		assert.Equal(t, want, got, "ext=%q", ext)
	}
}

func TestIsSubdomain(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		host    string
		want    bool
	}{
		{"wildcard basic", "*.example.com", "api.example.com", true},
		{"wildcard deep", "*.example.com", "v1.api.example.com", true},
		{"wildcard requires label", "*.example.com", "example.com", false},
		{"wildcard mismatch", "*.example.com", "example.org", false},

		{"exact match", "sub.example.com", "sub.example.com", true},
		{"exact mismatch sub-sub", "sub.example.com", "deep.sub.example.com", true},
		{"exact case+port insensitive", "SuB.ExAmPlE.CoM", "SUB.example.com:443", true},

		{"base domain exact", "example.com", "example.com", true},
		{"base domain includes subdomains", "example.com", "api.example.com", true},
		{"base domain mismatch suffix", "example.com", "badexample.com", false},

		{"empty pattern", "", "a.example.com", false},

		{"localhost wildcard", "*.localhost", "a.localhost", true},
		{"localhost wildcard with port", "*.localhost:4017", "a.localhost:4017", true},
		{"scheme+port normalized", "https://*.example.com:443", "api.example.com:443", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsSubdomain(tc.pattern, tc.host)
			assert.Equal(t, got, tc.want, tc.name)
		})
	}
}

func TestSetCORSHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	SetCORSHeaders(w)

	headers := w.Header()

	assert.Equal(t, "*", headers.Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "GET, OPTIONS", headers.Get("Access-Control-Allow-Methods"))
	assert.Equal(t, "Content-Type, Accept, Accept-Encoding", headers.Get("Access-Control-Allow-Headers"))
}

func TestStripWildCard(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"with wildcard prefix", "*.example.com", "example.com"},
		{"with wildcard and space", " *.example.com", "example.com"},
		{"trailing space after wildcard", "*.example.com ", "example.com"},
		{"both wildcard and space", " *.example.com ", "example.com"},
		{"no wildcard", "example.com", "example.com"},
		{"wildcard only", "*.", ""},
		{"empty string", "", ""},
		{"whitespace only", "   ", ""},
		{"no wildcard with space", " example.com ", "example.com"},
		{"multiple dots after wildcard", "*.sub.example.com", "sub.example.com"},
		{"just asterisk no dot", "*example.com", "*example.com"},
		{"dot no asterisk", ".example.com", ".example.com"},
		{"asterisk middle", "example*.com", "example*.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripWildCard(tt.input)
			assert.Equal(t, tt.expected, result, "StripWildCard(%q)", tt.input)
		})
	}
}

func TestDefaultAppPattern(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"https with domain", "https://portal.example.com", "*.portal.example.com"},
		{"http with domain", "http://portal.example.com", "*.portal.example.com"},
		{"domain only", "portal.example.com", "*.portal.example.com"},
		{"domain with port", "portal.example.com:4017", "*.portal.example.com:4017"},
		{"localhost with port", "localhost:4017", "*.localhost:4017"},
		{"empty string", "", "*.localhost:4017"},
		{"whitespace only", "   ", "*.localhost:4017"},
		{"trailing slash", "portal.example.com/", "*.portal.example.com"},
		{"already has wildcard", "*.example.com", "*.example.com"},
		{"https with port", "https://portal.example.com:443", "*.portal.example.com:443"},
		{"http with port", "http://portal.example.com:8080", "*.portal.example.com:8080"},
		{"with path keeps path", "https://portal.example.com/path", "*.portal.example.com/path"},
		{"just wildcard", "*.", "*.localhost:4017"},
		{"just scheme", "https://", "*.https:"},
		{"localhost no port", "localhost", "*.localhost"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DefaultAppPattern(tt.input)
			assert.Equal(t, tt.expected, result, "DefaultAppPattern(%q)", tt.input)
		})
	}
}

func TestStripPort_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty string", "", ""},
		{"no colon", "example.com", "example.com"},
		{"colon at end", "example.com:", "example.com:"},
		{"colon no port but path", "example.com:/path", "example.com:/path"},
		{"non-digit port", "example.com:abc", "example.com:abc"},
		{"mixed port", "example.com:12a34", "example.com:12a34"},
		{"multiple colons - last not all digits", "example.com:8080:extra", "example.com:8080:extra"},
		{"IPv6 with port", "[::1]:8080", "[::1]"},
		{"IPv6 no port", "[::1]", "[::1]"},
		{"just colon", ":", ":"},
		{"just digits after colon", ":8080", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripPort(tt.input)
			assert.Equal(t, tt.expected, result, "StripPort(%q)", tt.input)
		})
	}
}
