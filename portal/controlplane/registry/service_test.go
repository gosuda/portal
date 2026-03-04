package registry

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"testing"
	"time"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/controlplane"
	"gosuda.org/portal/types"
)

type fakeBackend struct {
	registerRouteErr   error
	leases             map[string]*portal.LeaseEntry
	baseHost           string
	connectLeaseID     string
	connectToken       string
	connectClientIP    string
	unregisteredLeases []string
	droppedLeases      []string
	updateLeaseAllowed bool
}

func newFakeBackend(baseHost string) *fakeBackend {
	return &fakeBackend{
		baseHost:           baseHost,
		updateLeaseAllowed: true,
		leases:             make(map[string]*portal.LeaseEntry),
	}
}

func (f *fakeBackend) BaseHost() string {
	return f.baseHost
}

func (f *fakeBackend) UpdateLease(lease *portal.Lease) bool {
	if !f.updateLeaseAllowed {
		return false
	}
	f.leases[lease.ID] = &portal.LeaseEntry{
		Lease:   lease,
		Expires: lease.Expires,
	}
	return true
}

func (f *fakeBackend) DeleteLease(leaseID string) bool {
	if _, ok := f.leases[leaseID]; !ok {
		return false
	}
	delete(f.leases, leaseID)
	return true
}

func (f *fakeBackend) GetLeaseByID(leaseID string) (*portal.LeaseEntry, bool) {
	entry, ok := f.leases[leaseID]
	return entry, ok
}

func (f *fakeBackend) ClearDropped(string) {}

func (f *fakeBackend) DropLease(leaseID string) {
	f.droppedLeases = append(f.droppedLeases, leaseID)
}

func (f *fakeBackend) RegisterRoute(_, _, _ string) error {
	return f.registerRouteErr
}

func (f *fakeBackend) UnregisterRouteByLeaseID(leaseID string) {
	f.unregisteredLeases = append(f.unregisteredLeases, leaseID)
}

func (f *fakeBackend) HandleConnect(_ net.Conn, leaseID, token, clientIP string) {
	f.connectLeaseID = leaseID
	f.connectToken = token
	f.connectClientIP = clientIP
}

func mustTLSState(t *testing.T, leaseID string) *tls.ConnectionState {
	t.Helper()

	identity, err := controlplane.IssueIdentity(leaseID)
	if err != nil {
		t.Fatalf("IssueIdentity returned error: %v", err)
	}

	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate returned error: %v", err)
	}
	return &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leaf},
	}
}

func TestNewServiceRequiresBackend(t *testing.T) {
	t.Parallel()

	if _, err := NewService(nil, Options{}); err == nil {
		t.Fatal("expected error for nil backend")
	}
}

func TestAdmitRejectsMissingLeaseID(t *testing.T) {
	t.Parallel()

	svc, err := NewService(newFakeBackend("example.com"), Options{})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	_, apiErr := svc.Admit(AdmissionInput{
		RawLeaseID:         " ",
		RawReverseToken:    "token",
		ConnectionTLSState: &tls.ConnectionState{},
	})
	if apiErr == nil {
		t.Fatal("expected admission error")
	}
	if apiErr.Code != "missing_lease_id" {
		t.Fatalf("error code = %q, want missing_lease_id", apiErr.Code)
	}
}

func TestAdmitRejectsBannedIP(t *testing.T) {
	t.Parallel()

	svc, err := NewService(newFakeBackend("example.com"), Options{})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	_, apiErr := svc.Admit(AdmissionInput{
		RawLeaseID:         "lease-1",
		RawReverseToken:    "token",
		IsClientIPBanned:   true,
		ConnectionTLSState: &tls.ConnectionState{},
	})
	if apiErr == nil {
		t.Fatal("expected admission error")
	}
	if apiErr.Code != "ip_banned" {
		t.Fatalf("error code = %q, want ip_banned", apiErr.Code)
	}
}

func TestAdmitRequiresExistingLeaseWhenRequested(t *testing.T) {
	t.Parallel()

	svc, err := NewService(newFakeBackend("example.com"), Options{})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	_, apiErr := svc.Admit(AdmissionInput{
		RawLeaseID:         "lease-1",
		RawReverseToken:    "token",
		RequireExisting:    true,
		ConnectionTLSState: mustTLSState(t, "lease-1"),
	})
	if apiErr == nil {
		t.Fatal("expected admission error")
	}
	if apiErr.Code != "lease_not_found" {
		t.Fatalf("error code = %q, want lease_not_found", apiErr.Code)
	}
}

func TestAdmitSuccessWithMatchingToken(t *testing.T) {
	t.Parallel()

	backend := newFakeBackend("example.com")
	backend.leases["lease-1"] = &portal.LeaseEntry{
		Lease: &portal.Lease{
			ID:           "lease-1",
			ReverseToken: "token-1",
		},
	}

	svc, err := NewService(backend, Options{})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	result, apiErr := svc.Admit(AdmissionInput{
		RawLeaseID:         " lease-1 ",
		RawReverseToken:    " token-1 ",
		ClientIP:           " 198.51.100.9 ",
		RequireExisting:    true,
		ConnectionTLSState: mustTLSState(t, "lease-1"),
	})
	if apiErr != nil {
		t.Fatalf("Admit returned error: %+v", apiErr)
	}
	if result.LeaseID != "lease-1" {
		t.Fatalf("lease id = %q, want lease-1", result.LeaseID)
	}
	if result.ReverseToken != "token-1" {
		t.Fatalf("reverse token = %q, want token-1", result.ReverseToken)
	}
	if result.ClientIP != "198.51.100.9" {
		t.Fatalf("client ip = %q, want 198.51.100.9", result.ClientIP)
	}
}

func TestAdmitRejectsInvalidTokenWithValidCertificate(t *testing.T) {
	t.Parallel()

	backend := newFakeBackend("example.com")
	backend.leases["lease-1"] = &portal.LeaseEntry{
		Lease: &portal.Lease{
			ID:           "lease-1",
			ReverseToken: "token-1",
		},
	}

	svc, err := NewService(backend, Options{})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	_, apiErr := svc.Admit(AdmissionInput{
		RawLeaseID:         "lease-1",
		RawReverseToken:    "wrong-token",
		RequireExisting:    true,
		ConnectionTLSState: mustTLSState(t, "lease-1"),
	})
	if apiErr == nil {
		t.Fatal("expected admission error")
	}
	if apiErr.Code != "unauthorized" {
		t.Fatalf("error code = %q, want unauthorized", apiErr.Code)
	}
}

func TestAdmit_ValidCert_Passes(t *testing.T) {
	t.Parallel()

	backend := newFakeBackend("example.com")
	backend.leases["lease-1"] = &portal.LeaseEntry{
		Lease: &portal.Lease{
			ID:           "lease-1",
			ReverseToken: "token-1",
		},
	}

	svc, err := NewService(backend, Options{})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	result, apiErr := svc.Admit(AdmissionInput{
		RawLeaseID:         "lease-1",
		RawReverseToken:    "token-1",
		RequireExisting:    true,
		ConnectionTLSState: mustTLSState(t, "lease-1"),
	})
	if apiErr != nil {
		t.Fatalf("Admit returned error: %+v", apiErr)
	}
	if result.LeaseID != "lease-1" {
		t.Fatalf("lease id = %q, want lease-1", result.LeaseID)
	}
}

func TestAdmit_InvalidCert_Rejected(t *testing.T) {
	t.Parallel()

	backend := newFakeBackend("example.com")
	backend.leases["lease-1"] = &portal.LeaseEntry{
		Lease: &portal.Lease{
			ID:           "lease-1",
			ReverseToken: "token-1",
		},
	}

	svc, err := NewService(backend, Options{})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	// Present a cert bound to a different lease ID.
	_, apiErr := svc.Admit(AdmissionInput{
		RawLeaseID:         "lease-1",
		RawReverseToken:    "token-1",
		RequireExisting:    true,
		ConnectionTLSState: mustTLSState(t, "lease-other"),
	})
	if apiErr == nil {
		t.Fatal("expected admission error for mismatched cert")
	}
	if apiErr.Code != "cert_lease_mismatch" {
		t.Fatalf("error code = %q, want cert_lease_mismatch", apiErr.Code)
	}
}

func TestAdmit_NoCert_TokenValid_Passes(t *testing.T) {
	t.Parallel()

	backend := newFakeBackend("example.com")
	backend.leases["lease-1"] = &portal.LeaseEntry{
		Lease: &portal.Lease{
			ID:           "lease-1",
			ReverseToken: "token-1",
		},
	}

	svc, err := NewService(backend, Options{})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	// Nil TLS state — CertBind skipped, token validation still applies.
	result, apiErr := svc.Admit(AdmissionInput{
		RawLeaseID:         "lease-1",
		RawReverseToken:    "token-1",
		RequireExisting:    true,
		ConnectionTLSState: nil,
	})
	if apiErr != nil {
		t.Fatalf("Admit returned error: %+v", apiErr)
	}
	if result.LeaseID != "lease-1" {
		t.Fatalf("lease id = %q, want lease-1", result.LeaseID)
	}
}

func TestAdmit_NoCert_TokenInvalid_Rejected(t *testing.T) {
	t.Parallel()

	backend := newFakeBackend("example.com")
	backend.leases["lease-1"] = &portal.LeaseEntry{
		Lease: &portal.Lease{
			ID:           "lease-1",
			ReverseToken: "token-1",
		},
	}

	svc, err := NewService(backend, Options{})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	// Nil TLS state — CertBind skipped, but token does not match.
	_, apiErr := svc.Admit(AdmissionInput{
		RawLeaseID:         "lease-1",
		RawReverseToken:    "wrong-token",
		RequireExisting:    true,
		ConnectionTLSState: nil,
	})
	if apiErr == nil {
		t.Fatal("expected admission error for invalid token")
	}
	if apiErr.Code != "unauthorized" {
		t.Fatalf("error code = %q, want unauthorized", apiErr.Code)
	}
}

func TestRegisterSuccess(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 4, 0, 0, 0, 0, time.UTC)
	backend := newFakeBackend("example.com")
	svc, err := NewService(backend, Options{
		LeaseTTL: 30 * time.Second,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	resp, apiErr := svc.Register(RegisterInput{
		LeaseID:      "lease-1",
		ReverseToken: "token-1",
		Name:         "demo",
		Metadata:     &types.Metadata{Owner: "owner"},
		TLS:          true,
		PortalURL:    "https://portal.example.com",
	})
	if apiErr != nil {
		t.Fatalf("Register returned error: %+v", apiErr)
	}
	if !resp.Success {
		t.Fatal("expected success response")
	}
	if resp.LeaseID != "lease-1" {
		t.Fatalf("lease id = %q, want lease-1", resp.LeaseID)
	}
	entry, ok := backend.GetLeaseByID("lease-1")
	if !ok {
		t.Fatal("expected lease to be persisted")
	}
	if got := entry.Lease.Expires; !got.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("lease expiry = %v, want %v", got, now.Add(30*time.Second))
	}
}

func TestRegisterDeletesLeaseWhenRouteRegistrationFails(t *testing.T) {
	t.Parallel()

	backend := newFakeBackend("example.com")
	backend.registerRouteErr = errors.New("register failed")
	svc, err := NewService(backend, Options{})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	_, apiErr := svc.Register(RegisterInput{
		LeaseID:      "lease-1",
		ReverseToken: "token-1",
		Name:         "demo",
		TLS:          true,
		PortalURL:    "https://portal.example.com",
	})
	if apiErr == nil {
		t.Fatal("expected register error")
	}
	if apiErr.Code != "sni_register_failed" {
		t.Fatalf("error code = %q, want sni_register_failed", apiErr.Code)
	}
	if _, ok := backend.GetLeaseByID("lease-1"); ok {
		t.Fatal("expected lease to be deleted after route registration failure")
	}
}

func TestRenewExtendsLease(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 4, 1, 0, 0, 0, time.UTC)
	backend := newFakeBackend("example.com")
	entry := &portal.LeaseEntry{
		Lease: &portal.Lease{
			ID:      "lease-1",
			Name:    "demo",
			Expires: now,
		},
	}
	backend.leases["lease-1"] = entry

	svc, err := NewService(backend, Options{
		LeaseTTL: 30 * time.Second,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	if apiErr := svc.Renew(entry); apiErr != nil {
		t.Fatalf("Renew returned error: %+v", apiErr)
	}
	if got := entry.Lease.Expires; !got.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("lease expiry = %v, want %v", got, now.Add(30*time.Second))
	}
}

func TestRenewResetsFutureExpiryFromNow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 4, 1, 0, 0, 0, time.UTC)
	originalExpiry := now.Add(5 * time.Minute)
	backend := newFakeBackend("example.com")
	entry := &portal.LeaseEntry{
		Lease: &portal.Lease{
			ID:      "lease-1",
			Name:    "demo",
			Expires: originalExpiry,
		},
	}
	backend.leases["lease-1"] = entry

	svc, err := NewService(backend, Options{
		LeaseTTL: 30 * time.Second,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	if apiErr := svc.Renew(entry); apiErr != nil {
		t.Fatalf("Renew returned error: %+v", apiErr)
	}

	want := now.Add(30 * time.Second)
	if got := entry.Lease.Expires; !got.Equal(want) {
		t.Fatalf("lease expiry = %v, want %v", got, want)
	}
	if !entry.Lease.Expires.Before(originalExpiry) {
		t.Fatalf("lease expiry = %v, want a value before original future expiry %v", entry.Lease.Expires, originalExpiry)
	}
}

func TestUnregisterDropsLeaseAndRoutes(t *testing.T) {
	t.Parallel()

	backend := newFakeBackend("example.com")
	backend.leases["lease-1"] = &portal.LeaseEntry{Lease: &portal.Lease{ID: "lease-1"}}
	svc, err := NewService(backend, Options{})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	svc.Unregister("lease-1")

	if _, ok := backend.GetLeaseByID("lease-1"); ok {
		t.Fatal("expected lease to be removed")
	}
	if len(backend.unregisteredLeases) != 1 || backend.unregisteredLeases[0] != "lease-1" {
		t.Fatalf("unregistered leases = %v, want [lease-1]", backend.unregisteredLeases)
	}
	if len(backend.droppedLeases) != 1 || backend.droppedLeases[0] != "lease-1" {
		t.Fatalf("dropped leases = %v, want [lease-1]", backend.droppedLeases)
	}
}

func TestDomainRequiresBaseHost(t *testing.T) {
	t.Parallel()

	svc, err := NewService(newFakeBackend(""), Options{})
	if err != nil {
		t.Fatalf("NewService returned error: %v", err)
	}

	_, apiErr := svc.Domain()
	if apiErr == nil {
		t.Fatal("expected domain error")
	}
	if apiErr.Code != "base_domain_missing" {
		t.Fatalf("error code = %q, want base_domain_missing", apiErr.Code)
	}
}
