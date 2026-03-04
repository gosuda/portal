package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/types"
)

// SDKRegistry handles HTTP API for client lease registration.
type SDKRegistry struct {
	ipManager         *manager.IPManager
	portalURL         string
	trustProxyHeaders bool
}

const sdkLeaseTTL = 30 * time.Second

func normalizeLeaseID(raw string) string {
	return strings.TrimSpace(raw)
}

func normalizeLeaseCredentials(leaseID, reverseToken string) (string, string) {
	return normalizeLeaseID(leaseID), strings.TrimSpace(reverseToken)
}

func lookupLeaseEntry(serv *portal.RelayServer, leaseID string) (*portal.LeaseEntry, bool) {
	if serv == nil {
		return nil, false
	}
	entry, ok := serv.GetLeaseManager().GetLeaseByID(normalizeLeaseID(leaseID))
	if !ok || entry == nil || entry.Lease == nil {
		return nil, false
	}
	return entry, true
}

func (r *SDKRegistry) extractClientIP(req *http.Request) string {
	return manager.ExtractClientIP(req, r.trustProxyHeaders)
}

func (r *SDKRegistry) isClientIPBanned(clientIP string) bool {
	return manager.IsIPBannedByPolicy(r.ipManager, clientIP)
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

func (r *SDKRegistry) validateLeaseCredentials(w http.ResponseWriter, leaseID, reverseToken string) bool {
	if leaseID == "" {
		writeAPIError(w, http.StatusBadRequest, "missing_lease_id", "lease_id is required")
		return false
	}
	if reverseToken == "" {
		writeAPIError(w, http.StatusBadRequest, "missing_reverse_token", "reverse_token is required")
		return false
	}
	return true
}

func isWebSocketUpgrade(req *http.Request) bool {
	if req == nil {
		return false
	}
	return hasForwardedToken(req.Header.Get("Upgrade"), "websocket")
}

// HandleSDKRequest routes /sdk/* requests.
func (r *SDKRegistry) HandleSDKRequest(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
	path := strings.TrimSuffix(req.URL.Path, "/")

	switch path {
	case types.PathSDKRegister:
		r.handleRegister(w, req, serv)
	case types.PathSDKUnregister:
		r.handleUnregister(w, req, serv)
	case types.PathSDKRenew:
		r.handleRenew(w, req, serv)
	case types.PathSDKDomain:
		r.handleDomain(w, req, serv)
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

	leaseID := req.URL.Query().Get("lease_id")
	token := req.Header.Get(portal.ReverseConnectTokenHeader)
	ctx, ok := r.admitControlPlane(w, req, serv, leaseID, token, admissionConfig{
		requireExistingLease: true,
	})
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

	serv.GetReverseHub().HandleConnect(conn, ctx.leaseID, ctx.token, ctx.clientIP)
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

	registerReq.Name = strings.TrimSpace(registerReq.Name)
	if !types.IsValidLeaseName(registerReq.Name) {
		writeAPIError(w, http.StatusBadRequest, "invalid_name", "name must be a DNS label (letters, digits, hyphen; no dots or underscores)")
		return
	}
	if !registerReq.TLS {
		writeAPIError(w, http.StatusBadRequest, "tls_required", "tls must be enabled")
		return
	}
	ctx, ok := r.admitControlPlane(w, req, serv, registerReq.LeaseID, registerReq.ReverseToken, admissionConfig{})
	if !ok {
		return
	}
	registerReq.LeaseID = ctx.leaseID
	registerReq.ReverseToken = ctx.token

	// Create lease
	lease := &portal.Lease{
		ID:           registerReq.LeaseID,
		Name:         registerReq.Name,
		Metadata:     registerReq.Metadata,
		Expires:      time.Now().Add(sdkLeaseTTL),
		TLS:          true,
		ReverseToken: registerReq.ReverseToken,
	}

	if !serv.GetLeaseManager().UpdateLease(lease) {
		writeAPIError(w, http.StatusConflict, "lease_rejected", "failed to register lease (name conflict or policy violation)")
		return
	}

	serv.GetReverseHub().ClearDropped(registerReq.LeaseID)

	sniName := types.BuildSNIName(registerReq.Name, serv.BaseHost)
	if sniName == "" {
		serv.GetLeaseManager().DeleteLease(registerReq.LeaseID)
		writeAPIError(w, http.StatusInternalServerError, "sni_name_invalid", "failed to build SNI route name")
		return
	}
	if err := serv.GetSNIRouter().RegisterRoute(sniName, registerReq.LeaseID, registerReq.Name); err != nil {
		serv.GetLeaseManager().DeleteLease(registerReq.LeaseID)
		writeAPIError(w, http.StatusInternalServerError, "sni_register_failed", fmt.Sprintf("failed to register SNI route: %v", err))
		return
	}

	log.Info().
		Str("lease_id", registerReq.LeaseID).
		Str("name", registerReq.Name).
		Bool("tls", true).
		Msg("[Registry] Lease registered")

	publicURL := types.ServicePublicURL(r.portalURL, registerReq.Name)

	writeAPIData(w, http.StatusOK, types.RegisterResponse{
		LeaseID:   registerReq.LeaseID,
		PublicURL: publicURL,
		Success:   true,
	})
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
	ctx, ok := r.admitControlPlane(w, req, serv, unregisterReq.LeaseID, unregisterReq.ReverseToken, admissionConfig{
		requireExistingLease: true,
	})
	if !ok {
		return
	}

	if serv.GetLeaseManager().DeleteLease(ctx.leaseID) {
		log.Info().
			Str("lease_id", ctx.leaseID).
			Msg("[Registry] Lease unregistered")
	}
	serv.GetSNIRouter().UnregisterRouteByLeaseID(ctx.leaseID)
	serv.GetReverseHub().DropLease(ctx.leaseID)

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

	ctx, ok := r.admitControlPlane(w, req, serv, renewReq.LeaseID, renewReq.ReverseToken, admissionConfig{
		requireExistingLease: true,
	})
	if !ok {
		return
	}

	entry := ctx.entry
	entry.Lease.Expires = time.Now().Add(sdkLeaseTTL)
	if !serv.GetLeaseManager().UpdateLease(entry.Lease) {
		writeAPIError(w, http.StatusInternalServerError, "renew_failed", "failed to renew lease")
		return
	}

	sniName := types.BuildSNIName(entry.Lease.Name, serv.BaseHost)
	if sniName == "" {
		log.Warn().
			Str("lease_id", entry.Lease.ID).
			Str("name", entry.Lease.Name).
			Str("base_host", serv.BaseHost).
			Msg("[Registry] Skipping SNI route refresh due to invalid SNI name")
	} else if err := serv.GetSNIRouter().RegisterRoute(sniName, entry.Lease.ID, entry.Lease.Name); err != nil {
		log.Warn().
			Err(err).
			Str("lease_id", entry.Lease.ID).
			Str("name", entry.Lease.Name).
			Msg("[Registry] Failed to refresh SNI route on renew")
	}

	writeAPIOK(w, http.StatusOK)
}

// handleDomain returns the relay's base domain for TLS certificate construction.
func (r *SDKRegistry) handleDomain(w http.ResponseWriter, _ *http.Request, serv *portal.RelayServer) {
	if serv.BaseHost == "" {
		writeAPIError(w, http.StatusServiceUnavailable, "base_domain_missing", "base domain not configured")
		return
	}
	writeAPIData(w, http.StatusOK, types.DomainResponse{
		Success:    true,
		BaseDomain: serv.BaseHost,
	})
}
