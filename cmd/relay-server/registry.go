package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	controlplaneregistry "gosuda.org/portal/portal/controlplane/registry"
	"gosuda.org/portal/portal/policy"
	"gosuda.org/portal/types"
)

var errRegistryBackendUnavailable = errors.New("registry backend unavailable")

// SDKRegistry handles HTTP API for client lease registration.
type SDKRegistry struct {
	ipManager         *policy.IPFilter
	portalURL         string
	trustProxyHeaders bool
}

// HandleSDKRequest routes /sdk/* requests.
func (r *SDKRegistry) HandleSDKRequest(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
	registryService, err := r.newService(serv)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "registry_unavailable", "registry service unavailable")
		return
	}

	path := strings.TrimSuffix(req.URL.Path, "/")
	switch path {
	case types.PathSDKRegister:
		r.handleRegister(w, req, registryService)
	case types.PathSDKUnregister:
		r.handleUnregister(w, req, registryService)
	case types.PathSDKRenew:
		r.handleRenew(w, req, registryService)
	case types.PathSDKDomain:
		r.handleDomain(w, registryService)
	case types.PathSDKConnect:
		r.handleConnect(w, req, registryService)
	default:
		http.NotFound(w, req)
	}
}

func (r *SDKRegistry) handleConnect(w http.ResponseWriter, req *http.Request, registryService *controlplaneregistry.Service) {
	if req.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	if isWebSocketUpgrade(req) {
		writeAPIError(w, http.StatusBadRequest, "unsupported_transport", "websocket transport is not supported")
		return
	}

	admission, ok := r.admitControlPlane(
		w,
		req,
		registryService,
		req.URL.Query().Get("lease_id"),
		req.Header.Get(portal.ReverseConnectTokenHeader),
		true,
	)
	if !ok {
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "hijacker_unavailable", "server does not support connection hijacking")
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "hijack_failed", "failed to hijack connection")
		return
	}
	if _, err := rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: keep-alive\r\n\r\n"); err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			log.Debug().Err(closeErr).Msg("[Registry] failed to close hijacked connection after write failure")
		}
		return
	}
	if err := rw.Flush(); err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			log.Debug().Err(closeErr).Msg("[Registry] failed to close hijacked connection after flush failure")
		}
		return
	}

	registryService.HandleConnect(conn, admission)
}

// handleRegister handles SDK lease registration requests.
func (r *SDKRegistry) handleRegister(w http.ResponseWriter, req *http.Request, registryService *controlplaneregistry.Service) {
	if !r.requireMethod(w, req, http.MethodPost) {
		return
	}

	var registerReq types.RegisterRequest
	if !r.decodeRequestBody(w, req, &registerReq, "[Registry] Failed to decode registration request") {
		return
	}

	admission, ok := r.admitControlPlane(
		w,
		req,
		registryService,
		registerReq.LeaseID,
		registerReq.ReverseToken,
		false,
	)
	if !ok {
		return
	}

	registerResp, apiErr := registryService.Register(controlplaneregistry.RegisterInput{
		LeaseID:      admission.LeaseID,
		ReverseToken: admission.ReverseToken,
		Name:         registerReq.Name,
		Metadata:     &registerReq.Metadata,
		TLS:          registerReq.TLS,
		PortalURL:    r.portalURL,
	})
	if !writeRegistryError(w, apiErr) {
		return
	}

	writeAPIData(w, http.StatusOK, registerResp)
}

// handleUnregister handles SDK lease unregistration requests.
func (r *SDKRegistry) handleUnregister(w http.ResponseWriter, req *http.Request, registryService *controlplaneregistry.Service) {
	if !r.requireMethod(w, req, http.MethodPost) {
		return
	}

	var unregisterReq types.UnregisterRequest
	if !r.decodeRequestBody(w, req, &unregisterReq, "[Registry] Failed to decode unregistration request") {
		return
	}

	admission, ok := r.admitControlPlane(
		w,
		req,
		registryService,
		unregisterReq.LeaseID,
		unregisterReq.ReverseToken,
		true,
	)
	if !ok {
		return
	}

	registryService.Unregister(admission.LeaseID)
	writeAPIOK(w, http.StatusOK)
}

// handleRenew handles SDK lease renewal requests (keepalive).
func (r *SDKRegistry) handleRenew(w http.ResponseWriter, req *http.Request, registryService *controlplaneregistry.Service) {
	if !r.requireMethod(w, req, http.MethodPost) {
		return
	}

	var renewReq types.RenewRequest
	if !r.decodeRequestBody(w, req, &renewReq, "[Registry] Failed to decode renewal request") {
		return
	}

	admission, ok := r.admitControlPlane(
		w,
		req,
		registryService,
		renewReq.LeaseID,
		renewReq.ReverseToken,
		true,
	)
	if !ok {
		return
	}

	if !writeRegistryError(w, registryService.Renew(admission.Entry)) {
		return
	}
	writeAPIOK(w, http.StatusOK)
}

// handleDomain returns the relay's base domain for TLS certificate construction.
func (r *SDKRegistry) handleDomain(w http.ResponseWriter, registryService *controlplaneregistry.Service) {
	domainResp, apiErr := registryService.Domain()
	if !writeRegistryError(w, apiErr) {
		return
	}
	writeAPIData(w, http.StatusOK, domainResp)
}

func (r *SDKRegistry) admitControlPlane(
	w http.ResponseWriter,
	req *http.Request,
	registryService *controlplaneregistry.Service,
	rawLeaseID, rawToken string,
	requireExistingLease bool,
) (controlplaneregistry.AdmissionResult, bool) {
	clientIP := policy.ExtractClientIP(req, r.trustProxyHeaders)
	admission, apiErr := registryService.Admit(controlplaneregistry.AdmissionInput{
		RawLeaseID:         rawLeaseID,
		RawReverseToken:    rawToken,
		ClientIP:           clientIP,
		IsClientIPBanned:   policy.IsIPBannedByPolicy(r.ipManager, clientIP),
		RequireExisting:    requireExistingLease,
		ConnectionTLSState: req.TLS,
	})
	if !writeRegistryError(w, apiErr) {
		return controlplaneregistry.AdmissionResult{}, false
	}
	return admission, true
}

func (r *SDKRegistry) newService(serv *portal.RelayServer) (*controlplaneregistry.Service, error) {
	if serv == nil {
		return nil, errRegistryBackendUnavailable
	}
	return controlplaneregistry.NewService(
		newRelayRegistryBackend(serv),
		controlplaneregistry.Options{
			LeaseTTL: controlplaneregistry.DefaultLeaseTTL,
		},
	)
}

func (r *SDKRegistry) requireMethod(w http.ResponseWriter, req *http.Request, method string) bool {
	if req.Method == method {
		return true
	}

	w.Header().Set("Allow", method)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func (r *SDKRegistry) decodeRequestBody(w http.ResponseWriter, req *http.Request, dst any, logMessage string) bool {
	if err := json.NewDecoder(req.Body).Decode(dst); err != nil {
		log.Error().Err(err).Msg(logMessage)
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return false
	}
	return true
}

func writeRegistryError(w http.ResponseWriter, apiErr *types.APIError) bool {
	if apiErr == nil {
		return true
	}
	writeAPIError(w, apiErr.StatusCode, apiErr.Code, apiErr.Message)
	return false
}

type relayRegistryBackend struct {
	serv *portal.RelayServer
}

func newRelayRegistryBackend(serv *portal.RelayServer) *relayRegistryBackend {
	return &relayRegistryBackend{serv: serv}
}

func (b *relayRegistryBackend) BaseHost() string {
	if b.serv == nil {
		return ""
	}
	return b.serv.BaseHost
}

func (b *relayRegistryBackend) UpdateLease(lease *types.Lease) bool {
	if b.serv == nil || b.serv.GetLeaseManager() == nil {
		return false
	}
	return b.serv.GetLeaseManager().UpdateLease(lease)
}

func (b *relayRegistryBackend) DeleteLease(leaseID string) bool {
	if b.serv == nil || b.serv.GetLeaseManager() == nil {
		return false
	}
	return b.serv.GetLeaseManager().DeleteLease(leaseID)
}

func (b *relayRegistryBackend) GetLeaseByID(leaseID string) (*types.LeaseEntry, bool) {
	if b.serv == nil || b.serv.GetLeaseManager() == nil {
		return nil, false
	}
	return b.serv.GetLeaseManager().GetLeaseByID(leaseID)
}

func (b *relayRegistryBackend) ClearDropped(leaseID string) {
	if b.serv == nil || b.serv.GetReverseHub() == nil {
		return
	}
	b.serv.GetReverseHub().ClearDropped(leaseID)
}

func (b *relayRegistryBackend) DropLease(leaseID string) {
	if b.serv == nil || b.serv.GetReverseHub() == nil {
		return
	}
	b.serv.GetReverseHub().DropLease(leaseID)
}

func (b *relayRegistryBackend) RegisterRoute(sniName, leaseID, name string) error {
	if b.serv == nil || b.serv.GetSNIRouter() == nil {
		return errRegistryBackendUnavailable
	}
	return b.serv.GetSNIRouter().RegisterRoute(sniName, leaseID, name)
}

func (b *relayRegistryBackend) UnregisterRouteByLeaseID(leaseID string) {
	if b.serv == nil || b.serv.GetSNIRouter() == nil {
		return
	}
	b.serv.GetSNIRouter().UnregisterRouteByLeaseID(leaseID)
}

func (b *relayRegistryBackend) HandleConnect(conn net.Conn, leaseID, token, clientIP string) {
	if b.serv == nil || b.serv.GetReverseHub() == nil {
		if conn != nil {
			if err := conn.Close(); err != nil {
				log.Debug().Err(err).Msg("[Registry] failed to close reverse connection after backend lookup failure")
			}
		}
		return
	}
	b.serv.GetReverseHub().HandleConnect(conn, leaseID, token, clientIP)
}
