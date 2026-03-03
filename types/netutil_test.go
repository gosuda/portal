package types

import "testing"

func TestNormalizeTargetAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{
			name: "host and port",
			in:   "localhost:3000",
			want: "localhost:3000",
		},
		{
			name: "url with scheme",
			in:   "http://localhost:3000",
			want: "localhost:3000",
		},
		{
			name:    "url missing host",
			in:      "http:///only-path",
			wantErr: true,
		},
		{
			name:    "empty",
			in:      " ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeTargetAddr(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for input %q", tt.in)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error for input %q: %v", tt.in, err)
			}

			if got != tt.want {
				t.Fatalf("NormalizeTargetAddr(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
