package portal

import "testing"

func TestReverseHubAuthorization(t *testing.T) {
	hub := NewReverseHub()

	if hub.isAuthorized("lease-1", "token-1") {
		t.Fatal("expected unauthorized when authorizer is not configured")
	}

	hub.SetAuthorizer(func(leaseID, token string) bool {
		return leaseID == "lease-1" && token == "token-1"
	})

	if !hub.isAuthorized("lease-1", "token-1") {
		t.Fatal("expected authorized")
	}
	if hub.isAuthorized("lease-1", "wrong-token") {
		t.Fatal("expected unauthorized for wrong token")
	}
}
