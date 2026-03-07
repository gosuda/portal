package main

import "testing"

func TestNormalizeTargetAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "host and port", raw: "localhost:8080", want: "localhost:8080"},
		{name: "host only", raw: "localhost", want: "localhost:80"},
		{name: "http url", raw: "http://localhost:8080", want: "localhost:8080"},
		{name: "https url", raw: "https://127.0.0.1", want: "127.0.0.1:80"},
		{name: "ipv6 host", raw: "::1", want: "[::1]:80"},
		{name: "url with path", raw: "http://localhost:8080/app", wantErr: true},
		{name: "url with query", raw: "http://localhost:8080/?x=1", wantErr: true},
		{name: "empty", raw: "   ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeTargetAddr(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeTargetAddr(%q) error = nil, want error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeTargetAddr(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeTargetAddr(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
