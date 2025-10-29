package portal

import (
	"testing"
	"time"

	"github.com/gosuda/portal/portal/core/proto/rdsec"
	"github.com/gosuda/portal/portal/core/proto/rdverb"
)

func TestLeaseManager_NameConflict(t *testing.T) {
	lm := NewLeaseManager(30 * time.Second)
	defer lm.Stop()

	// Create two different identities
	identity1 := &rdsec.Identity{
		Id:        "identity-1",
		PublicKey: []byte("public-key-1"),
	}

	identity2 := &rdsec.Identity{
		Id:        "identity-2",
		PublicKey: []byte("public-key-2"),
	}

	// Lease 1 with name "my-service"
	lease1 := &rdverb.Lease{
		Identity: identity1,
		Name:     "my-service",
		Alpn:     []string{"http/1.1"},
		Expires:  time.Now().Add(10 * time.Minute).Unix(),
	}

	// Lease 2 with the same name "my-service" but different identity
	lease2 := &rdverb.Lease{
		Identity: identity2,
		Name:     "my-service",
		Alpn:     []string{"http/1.1"},
		Expires:  time.Now().Add(10 * time.Minute).Unix(),
	}

	// First lease should succeed
	if !lm.UpdateLease(lease1, 1) {
		t.Fatal("First lease registration should succeed")
	}

	// Second lease with same name should fail (name conflict)
	if lm.UpdateLease(lease2, 2) {
		t.Fatal("Second lease registration should fail due to name conflict")
	}

	// Verify only first lease exists
	entry, exists := lm.GetLeaseByID(string(identity1.Id))
	if !exists {
		t.Fatal("First lease should exist")
	}
	if entry.Lease.Name != "my-service" {
		t.Errorf("Expected lease name 'my-service', got '%s'", entry.Lease.Name)
	}

	// Verify second lease was not added
	_, exists = lm.GetLeaseByID(string(identity2.Id))
	if exists {
		t.Fatal("Second lease should not exist due to name conflict")
	}
}

func TestLeaseManager_SameIdentityUpdate(t *testing.T) {
	lm := NewLeaseManager(30 * time.Second)
	defer lm.Stop()

	identity := &rdsec.Identity{
		Id:        "identity-1",
		PublicKey: []byte("public-key-1"),
	}

	// Initial lease with name "my-service"
	lease1 := &rdverb.Lease{
		Identity: identity,
		Name:     "my-service",
		Alpn:     []string{"http/1.1"},
		Expires:  time.Now().Add(10 * time.Minute).Unix(),
	}

	// Updated lease with same identity and same name
	lease2 := &rdverb.Lease{
		Identity: identity,
		Name:     "my-service",
		Alpn:     []string{"http/1.1", "h2"},
		Expires:  time.Now().Add(15 * time.Minute).Unix(),
	}

	// First registration
	if !lm.UpdateLease(lease1, 1) {
		t.Fatal("First lease registration should succeed")
	}

	// Update with same identity should succeed (no conflict)
	if !lm.UpdateLease(lease2, 1) {
		t.Fatal("Updating own lease should succeed")
	}

	// Verify lease was updated
	entry, exists := lm.GetLeaseByID(string(identity.Id))
	if !exists {
		t.Fatal("Lease should exist")
	}
	if len(entry.Lease.Alpn) != 2 {
		t.Errorf("Expected 2 ALPNs, got %d", len(entry.Lease.Alpn))
	}
}

func TestLeaseManager_EmptyNameAllowed(t *testing.T) {
	lm := NewLeaseManager(30 * time.Second)
	defer lm.Stop()

	identity1 := &rdsec.Identity{
		Id:        "identity-1",
		PublicKey: []byte("public-key-1"),
	}

	identity2 := &rdsec.Identity{
		Id:        "identity-2",
		PublicKey: []byte("public-key-2"),
	}

	// Both leases with empty names should succeed
	lease1 := &rdverb.Lease{
		Identity: identity1,
		Name:     "",
		Alpn:     []string{"http/1.1"},
		Expires:  time.Now().Add(10 * time.Minute).Unix(),
	}

	lease2 := &rdverb.Lease{
		Identity: identity2,
		Name:     "",
		Alpn:     []string{"http/1.1"},
		Expires:  time.Now().Add(10 * time.Minute).Unix(),
	}

	if !lm.UpdateLease(lease1, 1) {
		t.Fatal("First lease with empty name should succeed")
	}

	if !lm.UpdateLease(lease2, 2) {
		t.Fatal("Second lease with empty name should succeed (empty names don't conflict)")
	}
}

func TestLeaseManager_UnnamedAllowed(t *testing.T) {
	lm := NewLeaseManager(30 * time.Second)
	defer lm.Stop()

	identity1 := &rdsec.Identity{
		Id:        "identity-1",
		PublicKey: []byte("public-key-1"),
	}

	identity2 := &rdsec.Identity{
		Id:        "identity-2",
		PublicKey: []byte("public-key-2"),
	}

	// Both leases with "(unnamed)" should succeed
	lease1 := &rdverb.Lease{
		Identity: identity1,
		Name:     "(unnamed)",
		Alpn:     []string{"http/1.1"},
		Expires:  time.Now().Add(10 * time.Minute).Unix(),
	}

	lease2 := &rdverb.Lease{
		Identity: identity2,
		Name:     "(unnamed)",
		Alpn:     []string{"http/1.1"},
		Expires:  time.Now().Add(10 * time.Minute).Unix(),
	}

	if !lm.UpdateLease(lease1, 1) {
		t.Fatal("First lease with '(unnamed)' should succeed")
	}

	if !lm.UpdateLease(lease2, 2) {
		t.Fatal("Second lease with '(unnamed)' should succeed (unnamed don't conflict)")
	}
}

func TestLeaseManager_UnicodeNameConflict(t *testing.T) {
	lm := NewLeaseManager(30 * time.Second)
	defer lm.Stop()

	identity1 := &rdsec.Identity{
		Id:        "identity-1",
		PublicKey: []byte("public-key-1"),
	}

	identity2 := &rdsec.Identity{
		Id:        "identity-2",
		PublicKey: []byte("public-key-2"),
	}

	// Lease with Korean name
	lease1 := &rdverb.Lease{
		Identity: identity1,
		Name:     "한글서비스",
		Alpn:     []string{"http/1.1"},
		Expires:  time.Now().Add(10 * time.Minute).Unix(),
	}

	lease2 := &rdverb.Lease{
		Identity: identity2,
		Name:     "한글서비스", // Same Korean name
		Alpn:     []string{"http/1.1"},
		Expires:  time.Now().Add(10 * time.Minute).Unix(),
	}

	if !lm.UpdateLease(lease1, 1) {
		t.Fatal("First lease with Korean name should succeed")
	}

	if lm.UpdateLease(lease2, 2) {
		t.Fatal("Second lease with same Korean name should fail")
	}
}
