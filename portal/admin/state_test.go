package admin

import (
	"os"
	"path/filepath"
	"testing"

	"gosuda.org/portal/portal/policy"
)

func TestStateStoreRoundTrip(t *testing.T) {
	tempDir := t.TempDir()
	runtime := policy.NewRuntime()
	if err := runtime.Approver().SetMode(policy.ModeManual); err != nil {
		t.Fatalf("SetMode() error = %v", err)
	}
	runtime.Approver().Approve("lease-approved")
	runtime.Approver().Deny("lease-denied")
	runtime.BanLease("lease-banned")
	runtime.IPFilter().BanIP("203.0.113.10")

	settingsPath := filepath.Join(tempDir, "admin_settings.json")
	store := newStateStore(settingsPath)
	if err := store.Save(runtime); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("Stat(admin_settings.json) error = %v", err)
	}

	loaded := policy.NewRuntime()
	if err := store.Load(loaded); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got := loaded.Approver().Mode(); got != policy.ModeManual {
		t.Fatalf("loaded approval mode = %q, want %q", got, policy.ModeManual)
	}
	if !loaded.Approver().IsApproved("lease-approved") {
		t.Fatalf("loaded runtime missing approved lease")
	}
	if !loaded.Approver().IsDenied("lease-denied") {
		t.Fatalf("loaded runtime missing denied lease")
	}
	if !loaded.IsLeaseBanned("lease-banned") {
		t.Fatalf("loaded runtime missing banned lease")
	}
	if !loaded.IPFilter().IsIPBanned("203.0.113.10") {
		t.Fatalf("loaded runtime missing banned IP")
	}
}
