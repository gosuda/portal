package utils

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsURLSafeName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Valid names
		{"empty string", "", true},
		{"simple name", "my-service", true},
		{"with underscore", "my_service", true},
		{"with numbers", "service123", true},
		{"mixed case", "MyService", true},
		{"all hyphens", "my-cool-service", true},
		{"all underscores", "my_cool_service", true},
		{"alphanumeric only", "service", true},
		{"numbers only", "12345", true},
		{"korean", "한글서비스", true},
		{"korean with hyphen", "한글-서비스", true},
		{"korean with underscore", "한글_서비스", true},
		{"mixed korean english", "MyService한글", true},
		{"japanese", "日本語サービス", true},
		{"chinese", "中文服务", true},
		{"arabic", "خدمة", true},
		{"mixed languages", "Service-서비스-サービス", true},
		{"korean numbers", "서비스3", true},

		// Invalid names
		{"with space", "my service", false},
		{"with leading space", " service", false},
		{"with trailing space", "service ", false},
		{"with slash", "my/service", false},
		{"with dot", "my.service", false},
		{"with colon", "my:service", false},
		{"with question mark", "my?service", false},
		{"with ampersand", "my&service", false},
		{"with equals", "my=service", false},
		{"with percent", "my%service", false},
		{"with plus", "my+service", false},
		{"with asterisk", "my*service", false},
		{"with at", "my@service", false},
		{"with hash", "my#service", false},
		{"with exclamation", "my!service", false},
		{"with parentheses", "my(service)", false},
		{"with brackets", "my[service]", false},
		{"with braces", "my{service}", false},
		{"with semicolon", "my;service", false},
		{"with comma", "my,service", false},
		{"with quote", "my'service", false},
		{"with double quote", "my\"service", false},
		{"with backslash", "my\\service", false},
		{"with pipe", "my|service", false},
		{"with tilde", "my~service", false},
		{"with backtick", "my`service", false},
		{"with less than", "my<service", false},
		{"with greater than", "my>service", false},
		{"emoji", "my-service🚀", false},
		{"with space korean", "한 글서비스", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsURLSafeName(tt.input)
			assert.Equal(t, tt.expected, result, "isURLSafeName(%q)", tt.input)
		})
	}
}

func TestNormalizePortalURL(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		want       string
		shouldFail bool
	}{
		{
			name:  "legacy ws converted",
			input: "ws://localhost:4017/relay",
			want:  "http://localhost:4017/relay",
		},
		{
			name:  "legacy wss converted",
			input: "wss://localhost:4017/relay",
			want:  "https://localhost:4017/relay",
		},
		{
			name:  "localhost with port",
			input: "localhost:4017",
			want:  "https://localhost:4017/relay",
		},
		{
			name:  "domain without port",
			input: "example.com",
			want:  "https://example.com/relay",
		},
		{
			name:  "http scheme without path",
			input: "http://example.com",
			want:  "http://example.com/relay",
		},
		{
			name:  "https scheme without path",
			input: "https://example.com",
			want:  "https://example.com/relay",
		},
		{
			name:  "http scheme with path",
			input: "http://example.com/custom",
			want:  "http://example.com/custom",
		},
		{
			name:  "https scheme with path",
			input: "https://example.com/custom",
			want:  "https://example.com/custom",
		},
		{
			name:  "bare host with path",
			input: "example.com/custom",
			want:  "https://example.com/custom",
		},
		{
			name:       "empty",
			input:      "",
			shouldFail: true,
		},
		{
			name:       "whitespace only",
			input:      "   ",
			shouldFail: true,
		},
		{
			name:       "missing host",
			input:      "/relay",
			shouldFail: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizePortalURL(tt.input)
			if tt.shouldFail {
				assert.Error(t, err, "normalizeBootstrapServer(%q) expected error", tt.input)
				return
			}
			assert.NoError(t, err, "normalizeBootstrapServer(%q) unexpected error", tt.input)
			assert.Equal(t, tt.want, got, "normalizeBootstrapServer(%q)", tt.input)
		})
	}
}

func TestParseURLs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"spaces only", "   ", nil},
		{"single", "ws://a", []string{"ws://a"}},
		{"trim spaces", "  ws://a  ,  wss://b  ", []string{"ws://a", "wss://b"}},
		{"ignore empties", ",,ws://a,,wss://b,,", []string{"ws://a", "wss://b"}},
		{"three", "a,b,c", []string{"a", "b", "c"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseURLs(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetContentType(t *testing.T) {
	cases := map[string]string{
		".html": "text/html; charset=utf-8",
		".js":   "application/javascript",
		".json": "application/json",
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

// Tests for http.go functions

func TestIsHTMLContentType(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		expected    bool
	}{
		// Valid HTML content types
		{"simple html", "text/html", true},
		{"html with charset", "text/html; charset=utf-8", true},
		{"HTML uppercase", "TEXT/HTML", true},
		{"HTML mixed case", "Text/HTML", true},
		{"html with charset and space", "text/html ; charset=utf-8", true},
		{"html with multiple params", "text/html; charset=utf-8; version=1", true},

		// Invalid content types (fallback to prefix check on parse error)
		{"malformed with prefix", "text/html;bad", true},
		{"malformed without prefix", "application/json", false},

		// Non-HTML content types
		{"json", "application/json", false},
		{"plain text", "text/plain", false},
		{"css", "text/css", false},
		{"javascript", "application/javascript", false},
		{"xml", "application/xml", false},

		// Edge cases
		{"empty string", "", false},
		{"whitespace", "   ", false},
		{"just text/html prefix", "text/htmlextra", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsHTMLContentType(tt.contentType)
			assert.Equal(t, tt.expected, result, "IsHTMLContentType(%q)", tt.contentType)
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

func TestIsLocalhost(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		expected   bool
	}{
		// IPv4 loopback
		{"127.0.0.1", "127.0.0.1:1234", true},
		{"127.0.0.2", "127.0.0.2:8080", true},
		{"127.1.1.1", "127.1.1.1:9999", true},

		// IPv6 loopback
		{"::1", "[::1]:8080", true},
		{"ipv6 loopback with zone", "[::1%lo0]:8080", true},

		// Private IP ranges
		{"10.0.0.1", "10.0.0.1:1234", true},
		{"172.16.0.1", "172.16.0.1:5678", true},
		{"192.168.1.1", "192.168.1.1:9999", true},

		// Docker Desktop host alias
		{"host.docker.internal", "host.docker.internal:1234", true},
		{"HOST.DOCKER.INTERNAL", "HOST.DOCKER.INTERNAL:8080", true},

		// Public IPs
		{"8.8.8.8", "8.8.8.8:1234", false},
		{"1.1.1.1", "1.1.1.1:5678", false},

		// Hostnames (best-effort resolution - may vary by environment)
		{"localhost", "localhost:8080", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.remoteAddr

			result := IsLocalhost(req)

			// For hostname tests, only assert if we expect true
			// DNS resolution may vary by environment
			if tt.expected && !strings.Contains(tt.remoteAddr, ":") && tt.remoteAddr != "host.docker.internal" && !strings.HasPrefix(tt.remoteAddr, "127.") && !strings.HasPrefix(tt.remoteAddr, "[::1]") && !strings.HasPrefix(tt.remoteAddr, "10.") && !strings.HasPrefix(tt.remoteAddr, "172.16.") && !strings.HasPrefix(tt.remoteAddr, "192.168.") {
				// For best-effort hostname tests, just check it doesn't panic
				assert.NotPanics(t, func() { IsLocalhost(req) })
			} else {
				assert.Equal(t, tt.expected, result, "IsLocalhost(%q)", tt.remoteAddr)
			}
		})
	}
}

// Tests for url.go functions

func TestIsHexString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Valid hex strings
		{"empty string", "", true},
		{"single digit", "0", true},
		{"single lowercase", "a", true},
		{"single uppercase", "A", true},
		{"all digits", "1234567890", true},
		{"all lowercase", "abcdef", true},
		{"all uppercase", "ABCDEF", true},
		{"mixed case", "aAbBcCdDeEfF", true},
		{"with leading zeros", "00aabb", true},
		{"common hex", "deadbeef", true},
		{"long hex", "1234567890abcdefABCDEF", true},

		// Invalid hex strings
		{"with space", "abc def", false},
		{"with g", "abcdefg", false},
		{"with G", "ABCDEFG", false},
		{"with special char", "abc@def", false},
		{"with punctuation", "abc.def", false},
		{"with newline", "abc\ndef", false},
		{"with tab", "abc\tdef", false},
		{"unicode", "한글", false},
		{"emoji", "🚀", false},
		{"minus", "-abc", false},
		{"plus", "+abc", false},
		{"underscore", "abc_def", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsHexString(tt.input)
			assert.Equal(t, tt.expected, result, "IsHexString(%q)", tt.input)
		})
	}
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

func TestDefaultBootstrapFrom(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty string", "", "http://localhost:4017/relay"},
		{"whitespace", "   ", "http://localhost:4017/relay"},
		{"localhost with port", "localhost:4017", "https://localhost:4017/relay"},
		{"https with domain", "https://portal.example.com", "https://portal.example.com/relay"},
		{"http with domain", "http://portal.example.com", "http://portal.example.com/relay"},
		{"legacy ws scheme", "ws://example.com", "http://example.com/relay"},
		{"legacy wss scheme", "wss://example.com", "https://example.com/relay"},
		{"legacy ws with path", "ws://example.com/relay", "http://example.com/relay"},
		{"legacy wss with path", "wss://example.com/relay", "https://example.com/relay"},
		{"domain only", "example.com", "https://example.com/relay"},
		{"with trailing slash", "example.com/", "https://example.com/relay"},
		{"with path", "example.com/custom", "https://example.com/custom"},
		{"edge case invalid url", "://invalid", "https://://invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DefaultBootstrapFrom(tt.input)
			assert.Equal(t, tt.expected, result, "DefaultBootstrapFrom(%q)", tt.input)
		})
	}
}

// Additional edge case tests for improved coverage

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
