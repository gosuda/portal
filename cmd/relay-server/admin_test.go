package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/types"
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

func TestHandleAdminRequestBannedLeasesReturnsPlainIDs(t *testing.T) {
	serv, err := portal.NewRelayServer(context.Background(), nil, ":0", "portal.example.com", "", "")
	if err != nil {
		t.Fatalf("create relay server: %v", err)
	}
	authManager := manager.NewAuthManager("test-secret")
	admin := NewAdmin(0, NewFrontend(), authManager)

	serv.GetLeaseManager().BanLease("lease-a")
	serv.GetLeaseManager().BanLease("lease-b")

	req := httptest.NewRequest(http.MethodGet, "/admin/leases/banned", http.NoBody)
	req.AddCookie(&http.Cookie{
		Name:  adminCookieName,
		Value: authManager.CreateSession(),
		Path:  "/admin",
	})
	rec := httptest.NewRecorder()

	admin.HandleAdminRequest(rec, req, serv)

	if rec.Code != http.StatusOK {
		t.Fatalf("HandleAdminRequest status = %d, want %d", rec.Code, http.StatusOK)
	}

	var envelope types.APIRawEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v (body=%q)", err, rec.Body.String())
	}
	if !envelope.OK {
		t.Fatalf("expected success envelope, got %+v", envelope)
	}

	var banned []string
	if err := json.Unmarshal(envelope.Data, &banned); err != nil {
		t.Fatalf("decode banned leases: %v", err)
	}
	slices.Sort(banned)
	want := []string{"lease-a", "lease-b"}
	if !slices.Equal(banned, want) {
		t.Fatalf("banned leases = %v, want %v", banned, want)
	}
}
