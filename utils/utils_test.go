package utils

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeRelayURLs(t *testing.T) {
	t.Parallel()

	got, err := NormalizeRelayURLs(
		" localhost:4017 , https://relay.example.com/base/relay?x=1#frag ",
		"https://relay.example.com/base",
	)
	if err != nil {
		t.Fatalf("NormalizeRelayURLs() error = %v", err)
	}

	want := []string{
		"https://localhost:4017",
		"https://relay.example.com/base",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeRelayURLs() = %v, want %v", got, want)
	}
}

func TestParseCIDRs(t *testing.T) {
	t.Parallel()

	got, err := ParseCIDRs("10.0.0.0/8, 10.0.0.0/8, 192.168.0.0/16")
	if err != nil {
		t.Fatalf("ParseCIDRs() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ParseCIDRs() len = %d, want %d", len(got), 2)
	}
}

func TestParseCIDRsRejectsInvalidValue(t *testing.T) {
	t.Parallel()

	if _, err := ParseCIDRs("not-a-cidr"); err == nil {
		t.Fatal("ParseCIDRs() error = nil, want invalid cidr error")
	}
}

func TestNormalizeTargetAddr(t *testing.T) {
	t.Parallel()

	got, err := NormalizeTargetAddr("http://127.0.0.1")
	if err != nil {
		t.Fatalf("NormalizeTargetAddr() error = %v", err)
	}
	if got != "127.0.0.1:80" {
		t.Fatalf("NormalizeTargetAddr() = %q, want %q", got, "127.0.0.1:80")
	}
}

func TestNormalizeDNSLabel(t *testing.T) {
	t.Parallel()

	got, err := NormalizeDNSLabel("Demo-App")
	if err != nil {
		t.Fatalf("NormalizeDNSLabel() error = %v", err)
	}
	if got != "demo-app" {
		t.Fatalf("NormalizeDNSLabel() = %q, want %q", got, "demo-app")
	}
}

func TestLeaseHostname(t *testing.T) {
	t.Parallel()

	got, err := LeaseHostname("Demo-App", "portal.example.com")
	if err != nil {
		t.Fatalf("LeaseHostname() error = %v", err)
	}
	if got != "demo-app.portal.example.com" {
		t.Fatalf("LeaseHostname() = %q, want %q", got, "demo-app.portal.example.com")
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	if got := FormatDuration(90 * time.Second); got != "2m" {
		t.Fatalf("FormatDuration() = %q, want %q", got, "2m")
	}
}

func TestFormatLastSeen(t *testing.T) {
	t.Parallel()

	if got := FormatLastSeen(65 * time.Second); got != "1m 5s" {
		t.Fatalf("FormatLastSeen() = %q, want %q", got, "1m 5s")
	}
}

func TestDecodeBase64URLString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		encoded string
		want    string
	}{
		{encoded: "bGVhc2UtMTIz", want: "lease-123"},
		{encoded: "bGVhc2UtMTIzZA==", want: "lease-123d"},
		{encoded: "bGVhc2UtMTIzZA", want: "lease-123d"},
	}

	for _, tc := range cases {
		encoded, want := tc.encoded, tc.want
		got, err := DecodeBase64URLString(encoded)
		if err != nil {
			t.Fatalf("DecodeBase64URLString(%q) error = %v", encoded, err)
		}
		if got != want {
			t.Fatalf("DecodeBase64URLString(%q) = %q, want %q", encoded, got, want)
		}
	}
}

func TestDecodeBase64URLStringRejectsInvalidValue(t *testing.T) {
	t.Parallel()

	if _, err := DecodeBase64URLString("%%%"); err == nil {
		t.Fatal("DecodeBase64URLString() error = nil, want invalid base64 error")
	}
}

func TestRandomID(t *testing.T) {
	t.Parallel()

	got := RandomID("tok_")
	if !strings.HasPrefix(got, "tok_") {
		t.Fatalf("RandomID() = %q, want tok_ prefix", got)
	}
	if len(got) != len("tok_")+16 {
		t.Fatalf("RandomID() length = %d, want %d", len(got), len("tok_")+16)
	}
}

func TestRandomHex(t *testing.T) {
	t.Parallel()

	got, err := RandomHex(16)
	if err != nil {
		t.Fatalf("RandomHex() error = %v", err)
	}
	if len(got) != 32 {
		t.Fatalf("RandomHex() length = %d, want %d", len(got), 32)
	}
}

func TestSleepOrDoneCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if SleepOrDone(ctx, time.Second) {
		t.Fatal("SleepOrDone() = true, want false for canceled context")
	}
}
