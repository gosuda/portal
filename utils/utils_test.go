package utils

import (
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
			name:  "already ws",
			input: "ws://localhost:4017/relay",
			want:  "ws://localhost:4017/relay",
		},
		{
			name:  "already wss",
			input: "wss://localhost:4017/relay",
			want:  "wss://localhost:4017/relay",
		},
		{
			name:  "localhost with port",
			input: "localhost:4017",
			want:  "wss://localhost:4017/relay",
		},
		{
			name:  "domain without port",
			input: "example.com",
			want:  "wss://example.com/relay",
		},
		{
			name:  "http scheme without path",
			input: "http://example.com",
			want:  "ws://example.com/relay",
		},
		{
			name:  "https scheme without path",
			input: "https://example.com",
			want:  "wss://example.com/relay",
		},
		{
			name:  "http scheme with path",
			input: "http://example.com/custom",
			want:  "ws://example.com/custom",
		},
		{
			name:  "https scheme with path",
			input: "https://example.com/custom",
			want:  "wss://example.com/custom",
		},
		{
			name:  "bare host with path",
			input: "example.com/custom",
			want:  "wss://example.com/custom",
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
		".wasm": "application/wasm",
		".css":  "text/css",
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
