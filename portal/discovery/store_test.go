package discovery

import (
	"strings"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/types"
)

func signedRelayDescriptor(t *testing.T, privateKey, relayURL string) types.RelayDescriptor {
	t.Helper()

	identity, err := ResolveIdentity(privateKey)
	if err != nil {
		t.Fatalf("ResolveIdentity() error = %v", err)
	}

	now := time.Now().UTC()
	desc, err := SignedDescriptor(types.RelayDescriptor{
		RelayID:         relayURL,
		OwnerAddress:    identity.Address,
		SignerPublicKey: identity.PublicKey,
		Sequence:        uint64(now.UnixMilli()),
		Version:         1,
		IssuedAt:        now,
		ExpiresAt:       now.Add(time.Hour),
		APIHTTPSAddr:    relayURL,
		SupportsTCP:     true,
		StatusState:     "healthy",
	}, identity.PrivateKey)
	if err != nil {
		t.Fatalf("SignedDescriptor() error = %v", err)
	}
	return desc
}

func TestCacheRecordVerifiedReportsDescriptorChanges(t *testing.T) {
	t.Parallel()

	cache := NewCache()
	if _, err := cache.UpsertSeedURLs([]string{"https://relay-a.example.com"}); err != nil {
		t.Fatalf("UpsertSeedURLs() error = %v", err)
	}

	desc := signedRelayDescriptor(t, strings.Repeat("11", 32), "https://relay-a.example.com")
	if err := cache.PinIdentity(desc.RelayID, desc.APIHTTPSAddr, desc); err != nil {
		t.Fatalf("PinIdentity() error = %v", err)
	}

	added, changed, err := cache.RecordVerified(desc, true)
	if err != nil {
		t.Fatalf("RecordVerified() error = %v", err)
	}
	if added || !changed {
		t.Fatalf("RecordVerified() = added:%v changed:%v, want false true", added, changed)
	}

	updated := desc
	updated.StatusState = "degraded"
	updated.DescriptorSignature, err = SignDescriptor(updated, strings.Repeat("11", 32))
	if err != nil {
		t.Fatalf("SignDescriptor() error = %v", err)
	}

	added, changed, err = cache.RecordVerified(updated, true)
	if err != nil {
		t.Fatalf("RecordVerified() second error = %v", err)
	}
	if added || !changed {
		t.Fatalf("RecordVerified() second = added:%v changed:%v, want false true", added, changed)
	}
}

func TestCacheKnownDescriptorsIncludeExpiredForRehydration(t *testing.T) {
	t.Parallel()

	cache := NewCache()
	if _, err := cache.UpsertSeedURLs([]string{"https://relay-a.example.com"}); err != nil {
		t.Fatalf("UpsertSeedURLs() error = %v", err)
	}
	if !cache.Expire("https://relay-a.example.com") {
		t.Fatal("Expire() = false, want true")
	}

	known := cache.KnownDescriptors()
	if len(known) != 1 || known[0].RelayID != "https://relay-a.example.com" {
		t.Fatalf("KnownDescriptors() = %+v, want expired relay retained for rehydration", known)
	}
}
