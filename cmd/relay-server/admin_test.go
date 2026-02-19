package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
)

type adminTestEnv struct {
	admin        *Admin
	server       *portal.RelayServer
	settingsPath string
	authKey      string
}

func newAdminTestEnv(t *testing.T) *adminTestEnv {
	t.Helper()
	return newAdminTestEnvWithPath(t, filepath.Join(t.TempDir(), "admin_settings.json"))
}

func newAdminTestEnvWithPath(t *testing.T, settingsPath string) *adminTestEnv {
	t.Helper()

	cred, err := cryptoops.NewCredential()
	if err != nil {
		t.Fatalf("cryptoops.NewCredential: %v", err)
	}

	server := portal.NewRelayServer(cred, []string{"127.0.0.1:4017"})
	authKey := "test-admin-secret"
	admin := NewAdmin(0, nil, manager.NewAuthManager(authKey), "", "")
	admin.SetSettingsPath(settingsPath)

	return &adminTestEnv{
		admin:        admin,
		server:       server,
		settingsPath: settingsPath,
		authKey:      authKey,
	}
}

func (e *adminTestEnv) doRequest(
	t *testing.T,
	method, target, body string,
	cookies ...*http.Cookie,
) *httptest.ResponseRecorder {
	t.Helper()

	var requestBody io.Reader = http.NoBody
	if body != "" {
		requestBody = bytes.NewBufferString(body)
	}

	req := httptest.NewRequest(method, target, requestBody)
	req.RemoteAddr = "198.51.100.10:1234"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	for _, c := range cookies {
		req.AddCookie(c)
	}

	rec := httptest.NewRecorder()
	e.admin.HandleAdminRequest(rec, req, e.server)
	return rec
}

func (e *adminTestEnv) login(t *testing.T) *http.Cookie {
	t.Helper()

	rec := e.doRequest(t, http.MethodPost, "/admin/login", `{"key":"`+e.authKey+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}

	return responseCookieByName(t, rec.Result(), adminCookieName)
}

func decodeJSONResponse(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("json.Unmarshal response body %q: %v", rec.Body.String(), err)
	}
}

func responseCookieByName(t *testing.T, resp *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("expected cookie %q in response", name)
	return nil
}

func encodeLeaseID(leaseID string) string {
	return base64.URLEncoding.EncodeToString([]byte(leaseID))
}

func isLeaseBanned(server *portal.RelayServer, leaseID string) bool {
	for _, banned := range server.GetLeaseManager().GetBannedLeases() {
		if string(banned) == leaseID {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
}

func TestAdminAuthFlowAndProtectedRouteUnauthorized(t *testing.T) {
	env := newAdminTestEnv(t)

	authStatusBefore := env.doRequest(t, http.MethodGet, "/admin/auth/status", "")
	if authStatusBefore.Code != http.StatusOK {
		t.Fatalf("auth/status before login status = %d, want %d", authStatusBefore.Code, http.StatusOK)
	}
	var beforeStatus struct {
		Authenticated bool `json:"authenticated"`
		AuthEnabled   bool `json:"auth_enabled"`
	}
	decodeJSONResponse(t, authStatusBefore, &beforeStatus)
	if beforeStatus.Authenticated {
		t.Fatal("expected authenticated=false before login")
	}
	if !beforeStatus.AuthEnabled {
		t.Fatal("expected auth_enabled=true before login")
	}

	protectedUnauthed := env.doRequest(t, http.MethodGet, "/admin/settings", "")
	if protectedUnauthed.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized protected route status = %d, want %d", protectedUnauthed.Code, http.StatusUnauthorized)
	}
	if !strings.Contains(protectedUnauthed.Body.String(), "Unauthorized") {
		t.Fatalf("unauthorized body = %q, want to contain %q", protectedUnauthed.Body.String(), "Unauthorized")
	}

	badLogin := env.doRequest(t, http.MethodPost, "/admin/login", `{"key":"wrong-key"}`)
	if badLogin.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want %d", badLogin.Code, http.StatusUnauthorized)
	}

	cookie := env.login(t)

	authStatusAfterLogin := env.doRequest(t, http.MethodGet, "/admin/auth/status", "", cookie)
	if authStatusAfterLogin.Code != http.StatusOK {
		t.Fatalf("auth/status after login status = %d, want %d", authStatusAfterLogin.Code, http.StatusOK)
	}
	var afterLoginStatus struct {
		Authenticated bool `json:"authenticated"`
		AuthEnabled   bool `json:"auth_enabled"`
	}
	decodeJSONResponse(t, authStatusAfterLogin, &afterLoginStatus)
	if !afterLoginStatus.Authenticated {
		t.Fatal("expected authenticated=true after login")
	}
	if !afterLoginStatus.AuthEnabled {
		t.Fatal("expected auth_enabled=true after login")
	}

	logout := env.doRequest(t, http.MethodPost, "/admin/logout", "", cookie)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout status = %d, want %d", logout.Code, http.StatusOK)
	}
	clearedCookie := responseCookieByName(t, logout.Result(), adminCookieName)
	if clearedCookie.MaxAge >= 0 {
		t.Fatalf("logout cookie MaxAge = %d, want < 0", clearedCookie.MaxAge)
	}

	authStatusAfterLogout := env.doRequest(t, http.MethodGet, "/admin/auth/status", "", cookie)
	if authStatusAfterLogout.Code != http.StatusOK {
		t.Fatalf("auth/status after logout status = %d, want %d", authStatusAfterLogout.Code, http.StatusOK)
	}
	var afterLogoutStatus struct {
		Authenticated bool `json:"authenticated"`
		AuthEnabled   bool `json:"auth_enabled"`
	}
	decodeJSONResponse(t, authStatusAfterLogout, &afterLogoutStatus)
	if afterLogoutStatus.Authenticated {
		t.Fatal("expected authenticated=false after logout")
	}
}

func TestAdminApprovalModeEndpointValidation(t *testing.T) {
	env := newAdminTestEnv(t)
	cookie := env.login(t)

	getMode := env.doRequest(t, http.MethodGet, "/admin/settings/approval-mode", "", cookie)
	if getMode.Code != http.StatusOK {
		t.Fatalf("approval-mode GET status = %d, want %d", getMode.Code, http.StatusOK)
	}
	var getModeResp struct {
		Mode string `json:"approval_mode"`
	}
	decodeJSONResponse(t, getMode, &getModeResp)
	if getModeResp.Mode != string(manager.ApprovalModeAuto) {
		t.Fatalf("approval-mode GET = %q, want %q", getModeResp.Mode, manager.ApprovalModeAuto)
	}

	methodNotAllowed := env.doRequest(t, http.MethodPut, "/admin/settings/approval-mode", "", cookie)
	if methodNotAllowed.Code != http.StatusMethodNotAllowed {
		t.Fatalf("approval-mode PUT status = %d, want %d", methodNotAllowed.Code, http.StatusMethodNotAllowed)
	}

	invalidBody := env.doRequest(t, http.MethodPost, "/admin/settings/approval-mode", "{", cookie)
	if invalidBody.Code != http.StatusBadRequest {
		t.Fatalf("approval-mode invalid body status = %d, want %d", invalidBody.Code, http.StatusBadRequest)
	}

	invalidMode := env.doRequest(t, http.MethodPost, "/admin/settings/approval-mode", `{"mode":"invalid"}`, cookie)
	if invalidMode.Code != http.StatusBadRequest {
		t.Fatalf("approval-mode invalid mode status = %d, want %d", invalidMode.Code, http.StatusBadRequest)
	}

	setManual := env.doRequest(t, http.MethodPost, "/admin/settings/approval-mode", `{"mode":"manual"}`, cookie)
	if setManual.Code != http.StatusOK {
		t.Fatalf("approval-mode set manual status = %d, want %d body=%q", setManual.Code, http.StatusOK, setManual.Body.String())
	}
	var setManualResp struct {
		Mode string `json:"approval_mode"`
	}
	decodeJSONResponse(t, setManual, &setManualResp)
	if setManualResp.Mode != string(manager.ApprovalModeManual) {
		t.Fatalf("approval-mode set response = %q, want %q", setManualResp.Mode, manager.ApprovalModeManual)
	}
	if env.admin.GetApproveManager().GetApprovalMode() != manager.ApprovalModeManual {
		t.Fatalf("approval manager mode = %q, want %q", env.admin.GetApproveManager().GetApprovalMode(), manager.ApprovalModeManual)
	}
}

func TestAdminLeaseEndpointsMethodValidationAndState(t *testing.T) {
	env := newAdminTestEnv(t)
	cookie := env.login(t)

	leaseID := "lease-admin-test"
	encodedLeaseID := encodeLeaseID(leaseID)

	t.Run("ban endpoint", func(t *testing.T) {
		methodNotAllowed := env.doRequest(t, http.MethodPatch, "/admin/leases/"+encodedLeaseID+"/ban", "", cookie)
		if methodNotAllowed.Code != http.StatusMethodNotAllowed {
			t.Fatalf("ban PATCH status = %d, want %d", methodNotAllowed.Code, http.StatusMethodNotAllowed)
		}

		invalidLease := env.doRequest(t, http.MethodPost, "/admin/leases/*/ban", "", cookie)
		if invalidLease.Code != http.StatusBadRequest {
			t.Fatalf("ban invalid lease status = %d, want %d", invalidLease.Code, http.StatusBadRequest)
		}

		ban := env.doRequest(t, http.MethodPost, "/admin/leases/"+encodedLeaseID+"/ban", "", cookie)
		if ban.Code != http.StatusOK {
			t.Fatalf("ban POST status = %d, want %d", ban.Code, http.StatusOK)
		}
		if !isLeaseBanned(env.server, leaseID) {
			t.Fatalf("lease %q should be banned after POST", leaseID)
		}

		unban := env.doRequest(t, http.MethodDelete, "/admin/leases/"+encodedLeaseID+"/ban", "", cookie)
		if unban.Code != http.StatusOK {
			t.Fatalf("ban DELETE status = %d, want %d", unban.Code, http.StatusOK)
		}
		if isLeaseBanned(env.server, leaseID) {
			t.Fatalf("lease %q should be unbanned after DELETE", leaseID)
		}
	})

	t.Run("approve endpoint", func(t *testing.T) {
		methodNotAllowed := env.doRequest(t, http.MethodPatch, "/admin/leases/"+encodedLeaseID+"/approve", "", cookie)
		if methodNotAllowed.Code != http.StatusMethodNotAllowed {
			t.Fatalf("approve PATCH status = %d, want %d", methodNotAllowed.Code, http.StatusMethodNotAllowed)
		}

		invalidLease := env.doRequest(t, http.MethodPost, "/admin/leases/*/approve", "", cookie)
		if invalidLease.Code != http.StatusBadRequest {
			t.Fatalf("approve invalid lease status = %d, want %d", invalidLease.Code, http.StatusBadRequest)
		}

		approve := env.doRequest(t, http.MethodPost, "/admin/leases/"+encodedLeaseID+"/approve", "", cookie)
		if approve.Code != http.StatusOK {
			t.Fatalf("approve POST status = %d, want %d", approve.Code, http.StatusOK)
		}
		if !env.admin.GetApproveManager().IsLeaseApproved(leaseID) {
			t.Fatalf("lease %q should be approved after POST", leaseID)
		}
		if env.admin.GetApproveManager().IsLeaseDenied(leaseID) {
			t.Fatalf("lease %q should not be denied after approve POST", leaseID)
		}

		revoke := env.doRequest(t, http.MethodDelete, "/admin/leases/"+encodedLeaseID+"/approve", "", cookie)
		if revoke.Code != http.StatusOK {
			t.Fatalf("approve DELETE status = %d, want %d", revoke.Code, http.StatusOK)
		}
		if env.admin.GetApproveManager().IsLeaseApproved(leaseID) {
			t.Fatalf("lease %q should not be approved after approve DELETE", leaseID)
		}
	})

	t.Run("deny endpoint", func(t *testing.T) {
		methodNotAllowed := env.doRequest(t, http.MethodPatch, "/admin/leases/"+encodedLeaseID+"/deny", "", cookie)
		if methodNotAllowed.Code != http.StatusMethodNotAllowed {
			t.Fatalf("deny PATCH status = %d, want %d", methodNotAllowed.Code, http.StatusMethodNotAllowed)
		}

		invalidLease := env.doRequest(t, http.MethodPost, "/admin/leases/*/deny", "", cookie)
		if invalidLease.Code != http.StatusBadRequest {
			t.Fatalf("deny invalid lease status = %d, want %d", invalidLease.Code, http.StatusBadRequest)
		}

		deny := env.doRequest(t, http.MethodPost, "/admin/leases/"+encodedLeaseID+"/deny", "", cookie)
		if deny.Code != http.StatusOK {
			t.Fatalf("deny POST status = %d, want %d", deny.Code, http.StatusOK)
		}
		if !env.admin.GetApproveManager().IsLeaseDenied(leaseID) {
			t.Fatalf("lease %q should be denied after POST", leaseID)
		}
		if env.admin.GetApproveManager().IsLeaseApproved(leaseID) {
			t.Fatalf("lease %q should not be approved after deny POST", leaseID)
		}

		undeny := env.doRequest(t, http.MethodDelete, "/admin/leases/"+encodedLeaseID+"/deny", "", cookie)
		if undeny.Code != http.StatusOK {
			t.Fatalf("deny DELETE status = %d, want %d", undeny.Code, http.StatusOK)
		}
		if env.admin.GetApproveManager().IsLeaseDenied(leaseID) {
			t.Fatalf("lease %q should not be denied after deny DELETE", leaseID)
		}
	})

	t.Run("bps endpoint", func(t *testing.T) {
		methodNotAllowed := env.doRequest(t, http.MethodGet, "/admin/leases/"+encodedLeaseID+"/bps", "", cookie)
		if methodNotAllowed.Code != http.StatusMethodNotAllowed {
			t.Fatalf("bps GET status = %d, want %d", methodNotAllowed.Code, http.StatusMethodNotAllowed)
		}

		invalidLease := env.doRequest(t, http.MethodPost, "/admin/leases/*/bps", `{"bps":100}`, cookie)
		if invalidLease.Code != http.StatusBadRequest {
			t.Fatalf("bps invalid lease status = %d, want %d", invalidLease.Code, http.StatusBadRequest)
		}

		invalidBody := env.doRequest(t, http.MethodPost, "/admin/leases/"+encodedLeaseID+"/bps", "{", cookie)
		if invalidBody.Code != http.StatusBadRequest {
			t.Fatalf("bps invalid body status = %d, want %d", invalidBody.Code, http.StatusBadRequest)
		}

		setBPS := env.doRequest(t, http.MethodPost, "/admin/leases/"+encodedLeaseID+"/bps", `{"bps":4096}`, cookie)
		if setBPS.Code != http.StatusOK {
			t.Fatalf("bps POST status = %d, want %d", setBPS.Code, http.StatusOK)
		}
		if got := env.admin.GetBPSManager().GetBPSLimit(leaseID); got != 4096 {
			t.Fatalf("bps limit after POST = %d, want %d", got, 4096)
		}

		clearBPS := env.doRequest(t, http.MethodDelete, "/admin/leases/"+encodedLeaseID+"/bps", "", cookie)
		if clearBPS.Code != http.StatusOK {
			t.Fatalf("bps DELETE status = %d, want %d", clearBPS.Code, http.StatusOK)
		}
		if got := env.admin.GetBPSManager().GetBPSLimit(leaseID); got != 0 {
			t.Fatalf("bps limit after DELETE = %d, want %d", got, 0)
		}
	})
}

func TestAdminIPBanEndpointMethodValidationAndState(t *testing.T) {
	env := newAdminTestEnv(t)
	cookie := env.login(t)

	ip := "203.0.113.9"
	path := "/admin/ips/" + ip + "/ban"

	methodNotAllowed := env.doRequest(t, http.MethodGet, path, "", cookie)
	if methodNotAllowed.Code != http.StatusMethodNotAllowed {
		t.Fatalf("ip ban GET status = %d, want %d", methodNotAllowed.Code, http.StatusMethodNotAllowed)
	}

	invalidIP := env.doRequest(t, http.MethodPost, "/admin/ips//ban", "", cookie)
	if invalidIP.Code != http.StatusBadRequest {
		t.Fatalf("ip ban invalid IP status = %d, want %d", invalidIP.Code, http.StatusBadRequest)
	}

	ban := env.doRequest(t, http.MethodPost, path, "", cookie)
	if ban.Code != http.StatusOK {
		t.Fatalf("ip ban POST status = %d, want %d", ban.Code, http.StatusOK)
	}
	if !env.admin.GetIPManager().IsIPBanned(ip) {
		t.Fatalf("expected IP %q to be banned after POST", ip)
	}

	unban := env.doRequest(t, http.MethodDelete, path, "", cookie)
	if unban.Code != http.StatusOK {
		t.Fatalf("ip ban DELETE status = %d, want %d", unban.Code, http.StatusOK)
	}
	if env.admin.GetIPManager().IsIPBanned(ip) {
		t.Fatalf("expected IP %q to be unbanned after DELETE", ip)
	}
}

func TestAdminSettingsEndpointReturnsApprovalPayload(t *testing.T) {
	env := newAdminTestEnv(t)
	cookie := env.login(t)

	approvedLeaseID := "lease-settings-approved"
	deniedLeaseID := "lease-settings-denied"

	env.admin.GetApproveManager().SetApprovalMode(manager.ApprovalModeManual)
	env.admin.GetApproveManager().ApproveLease(approvedLeaseID)
	env.admin.GetApproveManager().DenyLease(deniedLeaseID)

	settings := env.doRequest(t, http.MethodGet, "/admin/settings", "", cookie)
	if settings.Code != http.StatusOK {
		t.Fatalf("settings GET status = %d, want %d", settings.Code, http.StatusOK)
	}

	var resp struct {
		ApprovalMode   string   `json:"approval_mode"`
		ApprovedLeases []string `json:"approved_leases"`
		DeniedLeases   []string `json:"denied_leases"`
	}
	decodeJSONResponse(t, settings, &resp)

	if resp.ApprovalMode != string(manager.ApprovalModeManual) {
		t.Fatalf("approval_mode = %q, want %q", resp.ApprovalMode, manager.ApprovalModeManual)
	}
	if !containsString(resp.ApprovedLeases, approvedLeaseID) {
		t.Fatalf("approved_leases %v should contain %q", resp.ApprovedLeases, approvedLeaseID)
	}
	if !containsString(resp.DeniedLeases, deniedLeaseID) {
		t.Fatalf("denied_leases %v should contain %q", resp.DeniedLeases, deniedLeaseID)
	}
}

func TestConvertLeaseEntriesToAdminRows(t *testing.T) {
	env := newAdminTestEnv(t)

	leaseID := "lease-convert-identity-123456"
	leaseIP := "203.0.113.42"
	lease := &rdverb.Lease{
		Identity: &rdsec.Identity{
			Id:        leaseID,
			PublicKey: []byte("public-key-convert"),
		},
		Name:     "",
		Alpn:     []string{"http/1.1"},
		Metadata: `{"hide":true}`,
		Expires:  time.Now().Add(2 * time.Minute).Unix(),
	}
	if !env.server.GetLeaseManager().UpdateLease(lease, 777) {
		t.Fatal("expected lease update to succeed")
	}

	entry, exists := env.server.GetLeaseManager().GetLeaseByID(leaseID)
	if !exists {
		t.Fatalf("expected lease %q to exist", leaseID)
	}
	entry.FirstSeen = time.Now().Add(-40 * time.Second)
	entry.LastSeen = time.Now().Add(-20 * time.Second)

	env.admin.GetBPSManager().SetBPSLimit(leaseID, 4096)
	env.admin.GetApproveManager().SetApprovalMode(manager.ApprovalModeManual)
	env.admin.GetApproveManager().DenyLease(leaseID)
	env.admin.GetIPManager().RegisterLeaseIP(leaseID, leaseIP)
	env.admin.GetIPManager().BanIP(leaseIP)

	rows := env.admin.convertLeaseEntriesToAdminRows(env.server)
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want %d", len(rows), 1)
	}
	row := rows[0]
	expectedUnnamed := "(" + "unnamed" + ")"

	if row.Peer != leaseID {
		t.Fatalf("peer = %q, want %q", row.Peer, leaseID)
	}
	if row.Name != expectedUnnamed {
		t.Fatalf("name = %q, want %q", row.Name, expectedUnnamed)
	}
	if row.Kind != "http/1.1" {
		t.Fatalf("kind = %q, want %q", row.Kind, "http/1.1")
	}
	if row.BPS != 4096 {
		t.Fatalf("bps = %d, want %d", row.BPS, 4096)
	}
	if row.IsApproved {
		t.Fatal("expected IsApproved=false")
	}
	if !row.IsDenied {
		t.Fatal("expected IsDenied=true")
	}
	if row.IP != leaseIP {
		t.Fatalf("ip = %q, want %q", row.IP, leaseIP)
	}
	if !row.IsIPBanned {
		t.Fatal("expected IsIPBanned=true")
	}
	if !row.StaleRed {
		t.Fatal("expected StaleRed=true for stale, disconnected lease")
	}
	if row.Connected {
		t.Fatal("expected Connected=false without active relay connection")
	}
	if !row.Hide {
		t.Fatal("expected Hide=true from parsed metadata")
	}
	if row.Metadata != lease.Metadata {
		t.Fatalf("metadata = %q, want %q", row.Metadata, lease.Metadata)
	}
	if row.TTL == "" {
		t.Fatal("expected TTL to be non-empty")
	}
	if _, err := time.Parse(time.RFC3339, row.FirstSeenISO); err != nil {
		t.Fatalf("FirstSeenISO parse error: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, row.LastSeenISO); err != nil {
		t.Fatalf("LastSeenISO parse error: %v", err)
	}
}

func TestAdminLeasesEndpointReturnsPopulatedRows(t *testing.T) {
	env := newAdminTestEnv(t)
	cookie := env.login(t)

	leaseID := "lease-http-list-123456"
	leaseIP := "198.51.100.88"
	lease := &rdverb.Lease{
		Identity: &rdsec.Identity{
			Id:        leaseID,
			PublicKey: []byte("public-key-http-list"),
		},
		Name:    "svc-http-list",
		Alpn:    []string{"h2"},
		Expires: time.Now().Add(2 * time.Minute).Unix(),
	}
	if !env.server.GetLeaseManager().UpdateLease(lease, 778) {
		t.Fatal("expected lease update to succeed")
	}

	env.admin.GetBPSManager().SetBPSLimit(leaseID, 1024)
	env.admin.GetApproveManager().SetApprovalMode(manager.ApprovalModeManual)
	env.admin.GetApproveManager().ApproveLease(leaseID)
	env.admin.GetIPManager().RegisterLeaseIP(leaseID, leaseIP)

	rec := env.doRequest(t, http.MethodGet, "/admin/leases", "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("leases GET status = %d, want %d body=%q", rec.Code, http.StatusOK, rec.Body.String())
	}

	var rows []leaseRow
	decodeJSONResponse(t, rec, &rows)
	if len(rows) != 1 {
		t.Fatalf("rows len = %d, want %d", len(rows), 1)
	}
	row := rows[0]

	if row.Peer != leaseID {
		t.Fatalf("peer = %q, want %q", row.Peer, leaseID)
	}
	if row.Name != lease.Name {
		t.Fatalf("name = %q, want %q", row.Name, lease.Name)
	}
	if row.Kind != "h2" {
		t.Fatalf("kind = %q, want %q", row.Kind, "h2")
	}
	if row.BPS != 1024 {
		t.Fatalf("bps = %d, want %d", row.BPS, 1024)
	}
	if !row.IsApproved {
		t.Fatal("expected IsApproved=true")
	}
	if row.IsDenied {
		t.Fatal("expected IsDenied=false")
	}
	if row.IP != leaseIP {
		t.Fatalf("ip = %q, want %q", row.IP, leaseIP)
	}
	if row.TTL == "" {
		t.Fatal("expected TTL to be non-empty")
	}
}

func TestSaveSettingsPersistsCurrentState(t *testing.T) {
	env := newAdminTestEnv(t)

	bannedLeaseID := "lease-banned"
	approvedLeaseID := "lease-approved"
	deniedLeaseID := "lease-denied"
	ip := "198.51.100.50"

	env.server.GetLeaseManager().BanLease(bannedLeaseID)
	env.admin.GetBPSManager().SetBPSLimit(bannedLeaseID, 2048)
	env.admin.GetApproveManager().SetApprovalMode(manager.ApprovalModeManual)
	env.admin.GetApproveManager().ApproveLease(approvedLeaseID)
	env.admin.GetApproveManager().DenyLease(deniedLeaseID)
	env.admin.GetIPManager().BanIP(ip)

	env.admin.SaveSettings(env.server)

	data, readErr := os.ReadFile(env.settingsPath)
	if readErr != nil {
		t.Fatalf("os.ReadFile(%q): %v", env.settingsPath, readErr)
	}

	var persisted adminSettings
	if unmarshalErr := json.Unmarshal(data, &persisted); unmarshalErr != nil {
		t.Fatalf("json.Unmarshal settings file: %v", unmarshalErr)
	}

	if !containsString(persisted.BannedLeases, bannedLeaseID) {
		t.Fatalf("persisted banned leases %v should contain %q", persisted.BannedLeases, bannedLeaseID)
	}
	if got := persisted.BPSLimits[bannedLeaseID]; got != 2048 {
		t.Fatalf("persisted BPS limit = %d, want %d", got, 2048)
	}
	if persisted.ApprovalMode != manager.ApprovalModeManual {
		t.Fatalf("persisted approval mode = %q, want %q", persisted.ApprovalMode, manager.ApprovalModeManual)
	}
	if !containsString(persisted.ApprovedLeases, approvedLeaseID) {
		t.Fatalf("persisted approved leases %v should contain %q", persisted.ApprovedLeases, approvedLeaseID)
	}
	if !containsString(persisted.DeniedLeases, deniedLeaseID) {
		t.Fatalf("persisted denied leases %v should contain %q", persisted.DeniedLeases, deniedLeaseID)
	}
	if !containsString(persisted.BannedIPs, ip) {
		t.Fatalf("persisted banned IPs %v should contain %q", persisted.BannedIPs, ip)
	}
}

func TestLoadSettingsRestoresRoundTripState(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "admin_settings.json")

	source := newAdminTestEnvWithPath(t, settingsPath)
	bannedLeaseID := "lease-load-banned"
	approvedLeaseID := "lease-load-approved"
	deniedLeaseID := "lease-load-denied"
	ip := "203.0.113.77"

	source.server.GetLeaseManager().BanLease(bannedLeaseID)
	source.admin.GetBPSManager().SetBPSLimit(bannedLeaseID, 8192)
	source.admin.GetApproveManager().SetApprovalMode(manager.ApprovalModeManual)
	source.admin.GetApproveManager().ApproveLease(approvedLeaseID)
	source.admin.GetApproveManager().DenyLease(deniedLeaseID)
	source.admin.GetIPManager().BanIP(ip)
	source.admin.SaveSettings(source.server)

	target := newAdminTestEnvWithPath(t, settingsPath)
	target.admin.LoadSettings(target.server)

	if !isLeaseBanned(target.server, bannedLeaseID) {
		t.Fatalf("loaded lease manager should contain banned lease %q", bannedLeaseID)
	}
	if got := target.admin.GetBPSManager().GetBPSLimit(bannedLeaseID); got != 8192 {
		t.Fatalf("loaded BPS limit = %d, want %d", got, 8192)
	}
	if mode := target.admin.GetApproveManager().GetApprovalMode(); mode != manager.ApprovalModeManual {
		t.Fatalf("loaded approval mode = %q, want %q", mode, manager.ApprovalModeManual)
	}
	if !target.admin.GetApproveManager().IsLeaseApproved(approvedLeaseID) {
		t.Fatalf("loaded approved leases should contain %q", approvedLeaseID)
	}
	if !target.admin.GetApproveManager().IsLeaseDenied(deniedLeaseID) {
		t.Fatalf("loaded denied leases should contain %q", deniedLeaseID)
	}
	if !target.admin.GetIPManager().IsIPBanned(ip) {
		t.Fatalf("loaded banned IPs should contain %q", ip)
	}
}
