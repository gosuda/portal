package utils

import "testing"

// Cross-language parity vectors -- keep in sync with frontend/src/lib/exposeName.test.ts
var exposeNameVectors = []struct {
	target, seed, expectedNormalized, expectedName string
}{
	{"3000", "test_seed", "127.0.0.1:3000", "bubble-cricket-beacon"},
	{"", "portal", "127.0.0.1:3000", "zesty-beacon-sketch"},
	{"http://localhost:8080", "cli_abc", "localhost:8080", "sprightly-rocket-zap"},
	{"192.168.1.1:8080", "web_xyz", "192.168.1.1:8080", "velvet-yeti-march"},
	{"localhost", "cli_", "localhost:80", "misty-rocket-ripple"},
}

func TestNormalizeExposeTarget(t *testing.T) {
	for _, v := range exposeNameVectors {
		got := normalizeExposeTarget(v.target)
		if got != v.expectedNormalized {
			t.Errorf("normalizeExposeTarget(%q) = %q, want %q", v.target, got, v.expectedNormalized)
		}
	}
}

func TestDefaultExposeName(t *testing.T) {
	for _, v := range exposeNameVectors {
		got, err := DefaultExposeName(v.target, v.seed)
		if err != nil {
			t.Errorf("DefaultExposeName(%q, %q) error: %v", v.target, v.seed, err)
			continue
		}
		if got != v.expectedName {
			t.Errorf("DefaultExposeName(%q, %q) = %q, want %q", v.target, v.seed, got, v.expectedName)
		}
	}
}
