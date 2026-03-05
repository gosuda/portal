package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/policy"
	"gosuda.org/portal/types"
)

// SDKRegistry handles HTTP API for client lease registration.
type SDKRegistry struct {
	ipManager         *policy.IPFilter
	portalURL         string
	trustProxyHeaders bool
}

// HandleSDKRequest routes /sdk/* requests.
func (r *SDKRegistry) HandleSDKRequest(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
	if serv == nil {
		writeAPIError(w, http.StatusInternalServerError, "registry_unavailable", "registry service unavailable")
		return
	}

	path := strings.TrimSuffix(req.URL.Path, "/")
	switch path {
	case types.PathSDKRegister:
		r.handleRegister(w, req, serv)
	case types.PathSDKUnregister:
		r.handleUnregister(w, req, serv)
	case types.PathSDKRenew:
		r.handleRenew(w, req, serv)
	case types.PathSDKDomain:
		r.handleDomain(w, serv)
	case types.PathSDKConnect:
		r.handleConnect(w, req, serv)
	default:
		http.NotFound(w, req)
	}
}

func (r *SDKRegistry) handleConnect(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
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
		serv,
		req.URL.Query().Get("lease_id"),
		req.Header.Get(types.ReverseConnectTokenHeader),
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

	serv.HandleRegistryConnect(conn, admission)
}

// handleRegister handles SDK lease registration requests.
func (r *SDKRegistry) handleRegister(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
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
		serv,
		registerReq.LeaseID,
		registerReq.ReverseToken,
		false,
	)
	if !ok {
		return
	}

	registerResp, apiErr := serv.RegisterLease(portal.RegistryRegisterInput{
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
func (r *SDKRegistry) handleUnregister(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
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
		serv,
		unregisterReq.LeaseID,
		unregisterReq.ReverseToken,
		true,
	)
	if !ok {
		return
	}

	serv.UnregisterLease(admission.LeaseID)
	writeAPIOK(w, http.StatusOK)
}

// handleRenew handles SDK lease renewal requests (keepalive).
func (r *SDKRegistry) handleRenew(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
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
		serv,
		renewReq.LeaseID,
		renewReq.ReverseToken,
		true,
	)
	if !ok {
		return
	}

	if !writeRegistryError(w, serv.RenewLease(admission.Entry)) {
		return
	}
	writeAPIOK(w, http.StatusOK)
}

// handleDomain returns the relay's base domain for TLS certificate construction.
func (r *SDKRegistry) handleDomain(w http.ResponseWriter, serv *portal.RelayServer) {
	domainResp, apiErr := serv.RegistryDomain()
	if !writeRegistryError(w, apiErr) {
		return
	}
	writeAPIData(w, http.StatusOK, domainResp)
}

func (r *SDKRegistry) admitControlPlane(
	w http.ResponseWriter,
	req *http.Request,
	serv *portal.RelayServer,
	rawLeaseID, rawToken string,
	requireExistingLease bool,
) (portal.RegistryAdmissionResult, bool) {
	clientIP := policy.ExtractClientIP(req, r.trustProxyHeaders)
	admission, apiErr := serv.AdmitControlPlane(portal.RegistryAdmissionInput{
		RawLeaseID:       rawLeaseID,
		RawReverseToken:  rawToken,
		ClientIP:         clientIP,
		IsClientIPBanned: policy.IsIPBannedByPolicy(r.ipManager, clientIP),
		RequireExisting:  requireExistingLease,
	})
	if !writeRegistryError(w, apiErr) {
		return portal.RegistryAdmissionResult{}, false
	}
	return admission, true
}

func (r *SDKRegistry) requireMethod(w http.ResponseWriter, req *http.Request, method string) bool {
	if req.Method == method {
		return true
	}

	w.Header().Set("Allow", method)
	writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	return false
}

func (r *SDKRegistry) decodeRequestBody(w http.ResponseWriter, req *http.Request, dst any, logMessage string) bool {
	req.Body = http.MaxBytesReader(w, req.Body, 1<<16)
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
