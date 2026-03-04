package admin

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/policy"
)

func TestServiceSaveLoadSettingsRoundTrip(t *testing.T) {
	service := NewService(0, policy.NewAuthenticator("test-secret"))
	settingsPath := filepath.Join(t.TempDir(), "admin_settings.json")
	service.SetSettingsPath(settingsPath)

	sourceServer := mustNewTestRelayServer(t)
	sourceServer.GetLeaseManager().BanLease("lease-ban")
	service.GetBPSManager().SetBPSLimit("lease-bps", 4096)
	service.GetApproveManager().SetApprovalMode(policy.ModeManual)
	service.GetApproveManager().ApproveLease("lease-approved")
	service.GetApproveManager().DenyLease("lease-denied")
	service.GetIPManager().BanIP("203.0.113.10")

	service.SaveSettings(sourceServer)

	targetServer := mustNewTestRelayServer(t)
	loaded := NewService(0, policy.NewAuthenticator("test-secret"))
	loaded.SetSettingsPath(settingsPath)
	loaded.LoadSettings(targetServer)

	if !contains(targetServer.GetLeaseManager().GetBannedLeases(), "lease-ban") {
		t.Fatalf("expected banned lease to be restored")
	}
	if got := loaded.GetBPSManager().GetBPSLimit("lease-bps"); got != 4096 {
		t.Fatalf("expected BPS limit 4096, got %d", got)
	}
	if loaded.GetApproveManager().GetApprovalMode() != policy.ModeManual {
		t.Fatalf("expected approval mode manual")
	}
	if !loaded.GetApproveManager().IsLeaseApproved("lease-approved") {
		t.Fatalf("expected approved lease to be restored")
	}
	if !loaded.GetApproveManager().IsLeaseDenied("lease-denied") {
		t.Fatalf("expected denied lease to be restored")
	}
	if !loaded.GetIPManager().IsIPBanned("203.0.113.10") {
		t.Fatalf("expected banned IP to be restored")
	}
}

func mustNewTestRelayServer(t *testing.T) *portal.RelayServer {
	t.Helper()

	server, err := portal.NewRelayServer(
		context.Background(),
		nil,
		":0",
		"localhost",
		t.TempDir(),
		"",
	)
	if err != nil {
		t.Fatalf("create relay server: %v", err)
	}
	t.Cleanup(server.Stop)
	return server
}

func contains(values []string, target string) bool {
	return slices.Contains(values, target)
}
