package main

import "testing"

func TestValidateRelayURLsForReverseConnect(t *testing.T) {
	tests := []struct {
		name      string
		relayURLs []string
		wantErr   bool
	}{
		{
			name:      "single https relay",
			relayURLs: []string{"https://relay.example.com"},
			wantErr:   false,
		},
		{
			name:      "multiple https relays",
			relayURLs: []string{"https://relay-a.example.com", "https://relay-b.example.com"},
			wantErr:   false,
		},
		{
			name:      "reject http relay",
			relayURLs: []string{"http://relay.example.com"},
			wantErr:   true,
		},
		{
			name:      "reject websocket relay",
			relayURLs: []string{"wss://relay.example.com"},
			wantErr:   true,
		},
		{
			name:      "reject malformed relay URL",
			relayURLs: []string{"://not-a-valid-url"},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRelayURLsForReverseConnect(tt.relayURLs)
			if tt.wantErr && err == nil {
				t.Fatalf("validateRelayURLsForReverseConnect(%v) expected error, got nil", tt.relayURLs)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateRelayURLsForReverseConnect(%v) unexpected error: %v", tt.relayURLs, err)
			}
		})
	}
}
