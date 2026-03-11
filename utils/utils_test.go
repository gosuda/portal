package utils

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeRelayURLs(t *testing.T) {
	t.Parallel()

	got, err := NormalizeRelayURLs([]string{
		" localhost:4017 , https://relay.example.com/base/relay?x=1#frag ",
		"https://relay.example.com/base",
	})
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
