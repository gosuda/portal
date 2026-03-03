package main

import (
	"encoding/base64"
	"testing"
)

func encodeLeaseIDForAdminRoute(leaseID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(leaseID))
}

func TestParseLeaseActionRoute(t *testing.T) {
	encodedLeaseID := encodeLeaseIDForAdminRoute("lease-123")

	tests := []struct {
		name       string
		route      string
		wantLease  string
		wantAction string
		wantStatus leaseActionRouteStatus
	}{
		{
			name:       "ban action",
			route:      "leases/" + encodedLeaseID + "/ban",
			wantLease:  "lease-123",
			wantAction: "ban",
			wantStatus: leaseActionRouteOK,
		},
		{
			name:       "bps action",
			route:      "leases/" + encodedLeaseID + "/bps",
			wantLease:  "lease-123",
			wantAction: "bps",
			wantStatus: leaseActionRouteOK,
		},
		{
			name:       "approve action",
			route:      "leases/" + encodedLeaseID + "/approve",
			wantLease:  "lease-123",
			wantAction: "approve",
			wantStatus: leaseActionRouteOK,
		},
		{
			name:       "deny action",
			route:      "leases/" + encodedLeaseID + "/deny",
			wantLease:  "lease-123",
			wantAction: "deny",
			wantStatus: leaseActionRouteOK,
		},
		{
			name:       "unsupported action",
			route:      "leases/" + encodedLeaseID + "/noop",
			wantStatus: leaseActionRouteNotFound,
		},
		{
			name:       "invalid route shape",
			route:      "leases/" + encodedLeaseID,
			wantStatus: leaseActionRouteNotFound,
		},
		{
			name:       "invalid encoded lease id",
			route:      "leases/not_base64!/ban",
			wantAction: "ban",
			wantStatus: leaseActionRouteInvalidLeaseID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLease, gotAction, gotStatus := parseLeaseActionRoute(tt.route)

			if gotStatus != tt.wantStatus {
				t.Fatalf("parseLeaseActionRoute(%q) status=%v, want %v", tt.route, gotStatus, tt.wantStatus)
			}
			if gotLease != tt.wantLease {
				t.Fatalf("parseLeaseActionRoute(%q) leaseID=%q, want %q", tt.route, gotLease, tt.wantLease)
			}
			if gotAction != tt.wantAction {
				t.Fatalf("parseLeaseActionRoute(%q) action=%q, want %q", tt.route, gotAction, tt.wantAction)
			}
		})
	}
}
