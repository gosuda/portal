package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/quic-go/webtransport-go"
	"github.com/stretchr/testify/assert"

	"gosuda.org/portal/portal"
)

func TestHandleWebTransportRelayRequest_BansUsingForwardedIP(t *testing.T) {
	admin := NewAdmin(0, nil, nil, "", "")
	admin.GetIPManager().BanIP("198.51.100.9")

	req := httptest.NewRequest(http.MethodConnect, "https://relay.example/relay", http.NoBody)
	req.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.1")
	req.RemoteAddr = "203.0.113.77:4242"

	rec := httptest.NewRecorder()
	upgradeCalled := false
	handleSessionCalled := false

	handleWebTransportRelayRequest(
		rec,
		req,
		admin,
		func(http.ResponseWriter, *http.Request) (*webtransport.Session, error) {
			upgradeCalled = true
			return nil, nil
		},
		func(portal.Session) {
			handleSessionCalled = true
		},
	)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, upgradeCalled, "upgrade should not be called for banned forwarded IP")
	assert.False(t, handleSessionCalled, "handleSession should not be called for banned forwarded IP")
}

func TestHandleWebTransportRelayRequest_FailedUpgradeDoesNotPolluteAssociation(t *testing.T) {
	admin := NewAdmin(0, nil, nil, "", "")

	req := httptest.NewRequest(http.MethodConnect, "https://relay.example/relay", http.NoBody)
	req.Header.Set("X-Forwarded-For", "198.51.100.22, 10.0.0.2")
	req.RemoteAddr = "203.0.113.88:4343"

	rec := httptest.NewRecorder()
	handleSessionCalled := false

	handleWebTransportRelayRequest(
		rec,
		req,
		admin,
		func(http.ResponseWriter, *http.Request) (*webtransport.Session, error) {
			return nil, errors.New("upgrade failed")
		},
		func(portal.Session) {
			handleSessionCalled = true
		},
	)

	assert.False(t, handleSessionCalled, "handleSession should not be called after failed upgrade")
	assert.Empty(t, admin.GetIPManager().PopPendingIP(), "expected no pending IP after failed upgrade")
	assert.Empty(t, admin.GetIPManager().GetLeaseIP("lease-1"), "expected no lease association after failed upgrade")
}
