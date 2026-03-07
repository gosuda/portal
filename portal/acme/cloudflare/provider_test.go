package cloudflare

import (
	"context"
	"testing"
)

func TestChallengeProviderRequiresToken(t *testing.T) {
	t.Parallel()

	provider := New("")
	_, err := provider.ChallengeProvider(context.Background())
	if err == nil {
		t.Fatal("ChallengeProvider() error = nil, want error")
	}
}
