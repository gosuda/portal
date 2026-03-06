package policy

import "testing"

func TestNewAuthenticatorGeneratesSecretWhenEmpty(t *testing.T) {
	t.Parallel()

	auth := NewAuthenticator("")
	if auth == nil {
		t.Fatal("NewAuthenticator() = nil")
	}
	if !auth.AuthEnabled() {
		t.Fatal("AuthEnabled() = false, want true")
	}
	if auth.secretKey == "" {
		t.Fatal("secretKey = empty, want generated value")
	}
}
