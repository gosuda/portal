package main

import (
	"net/http"
	"strings"

	"gosuda.org/portal/portal"
	portaladmin "gosuda.org/portal/portal/admin"
	"gosuda.org/portal/portal/policy"
)

// Admin is a thin adapter between relay-server wiring and portal/admin handlers.
type Admin struct {
	service *portaladmin.Service
	handler *portaladmin.Handler
}

func NewAdmin(frontend *Frontend, authManager *policy.Authenticator, portalURL string, trustProxy bool) *Admin {
	service := portaladmin.NewService(authManager)
	normalizedPortalURL := strings.TrimSpace(portalURL)
	admin := &Admin{
		service: service,
	}

	serveStatic := func(w http.ResponseWriter, r *http.Request, appPath string, serv *portal.RelayServer) {
		if frontend == nil {
			http.NotFound(w, r)
			return
		}
		frontend.ServeAppStatic(w, r, appPath, serv)
	}

	admin.handler = portaladmin.NewHandler(portaladmin.HandlerConfig{
		Service:        service,
		TrustProxy:     trustProxy,
		ServeAppStatic: serveStatic,
		ListLeases: func(serv *portal.RelayServer) any {
			return convertLeaseEntriesToRows(serv, admin, true, normalizedPortalURL)
		},
		DecodeLeaseID:         decodeLeaseID,
		IsSecureRequest:       isSecureRequestWithPolicy,
		WriteAPIData:          writeAPIData,
		WriteAPIOK:            writeAPIOK,
		WriteAPIError:         writeAPIError,
		WriteAPIErrorWithData: writeAPIErrorWithData,
	})

	return admin
}

// GetApproveManager exposes the approval manager.
func (a *Admin) GetApproveManager() *policy.Approver {
	if a == nil || a.service == nil {
		return nil
	}
	return a.service.GetApproveManager()
}

// GetBPSManager exposes the BPS manager.
func (a *Admin) GetBPSManager() *policy.RateLimiter {
	if a == nil || a.service == nil {
		return nil
	}
	return a.service.GetBPSManager()
}

// GetIPManager exposes the IP manager.
func (a *Admin) GetIPManager() *policy.IPFilter {
	if a == nil || a.service == nil {
		return nil
	}
	return a.service.GetIPManager()
}

func (a *Admin) SetSettingsPath(path string) {
	if a == nil || a.service == nil {
		return
	}
	a.service.SetSettingsPath(path)
}

func (a *Admin) SaveSettings(serv *portal.RelayServer) {
	if a == nil || a.service == nil {
		return
	}
	a.service.SaveSettings(serv)
}

func (a *Admin) LoadSettings(serv *portal.RelayServer) {
	if a == nil || a.service == nil {
		return
	}
	a.service.LoadSettings(serv)
}

// HandleAdminRequest routes /admin/* requests.
func (a *Admin) HandleAdminRequest(w http.ResponseWriter, r *http.Request, serv *portal.RelayServer) {
	if a == nil || a.handler == nil {
		writeAPIError(w, http.StatusInternalServerError, "admin_handler_unavailable", "admin handler unavailable")
		return
	}
	a.handler.HandleAdminRequest(w, r, serv)
}
