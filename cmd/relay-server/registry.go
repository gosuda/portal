package main

import (
	"crypto/subtle"
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
	trustProxyHeaders bool
}

const sdkLeaseTTL = 30 * time.Second

func reverseTokenMatches(expected, provided string) bool {
	expected = strings.TrimSpace(expected)
	provided = strings.TrimSpace(provided)
	if expected == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

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
	if r.ipManager == nil || clientIP == "" {
		return false
	}
	return r.ipManager.IsIPBanned(clientIP)
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
	return strings.EqualFold(strings.TrimSpace(req.Header.Get("Upgrade")), "websocket")
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
	if !r.requireMethod(w, req, http.MethodGet) {
		return
	}

	leaseID, token := normalizeLeaseCredentials(
		req.URL.Query().Get("lease_id"),
		req.Header.Get(portal.ReverseConnectTokenHeader),
	)
	if leaseID == "" {
		http.Error(w, "missing lease_id", http.StatusBadRequest)
		return
	}
	if token == "" {
		http.Error(w, "missing reverse token", http.StatusUnauthorized)
		return
	}
	if isWebSocketUpgrade(req) {
		http.Error(w, "websocket transport is not supported", http.StatusBadRequest)
		return
	}

	clientIP := strings.TrimSpace(r.extractClientIP(req))
	if r.isClientIPBanned(clientIP) {
		http.Error(w, "ip is banned", http.StatusForbidden)
		return
	}

	entry, ok := lookupLeaseEntry(serv, leaseID)
	if !ok {
		http.Error(w, "lease not found", http.StatusNotFound)
		return
	}
	if !reverseTokenMatches(entry.Lease.ReverseToken, token) {
		http.Error(w, "unauthorized reverse connect", http.StatusUnauthorized)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "server does not support connection hijacking", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "failed to hijack connection", http.StatusInternalServerError)
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

	serv.GetReverseHub().HandleConnect(conn, leaseID, token, clientIP)
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

	registerReq.LeaseID, registerReq.ReverseToken = normalizeLeaseCredentials(registerReq.LeaseID, registerReq.ReverseToken)
	registerReq.Name = strings.TrimSpace(registerReq.Name)

	if !r.validateLeaseCredentials(w, registerReq.LeaseID, registerReq.ReverseToken) {
		return
	}
	name := registerReq.Name
	if !types.IsValidLeaseName(name) {
		writeAPIError(w, http.StatusBadRequest, "invalid_name", "name must be a DNS label (letters, digits, hyphen; no dots or underscores)")
		return
	}
	if !registerReq.TLS {
		writeAPIError(w, http.StatusBadRequest, "tls_required", "tls must be enabled")
		return
	}
	if r.isClientIPBanned(r.extractClientIP(req)) {
		writeAPIError(w, http.StatusForbidden, "ip_banned", "ip is banned")
		return
	}

	// Ownership semantics: re-registration of an existing lease ID requires the same reverse token.
	if entry, ok := lookupLeaseEntry(serv, registerReq.LeaseID); ok {
		if !reverseTokenMatches(entry.Lease.ReverseToken, registerReq.ReverseToken) {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "unauthorized lease registration")
			return
		}
	}

	// Create lease
	lease := &portal.Lease{
		ID:           registerReq.LeaseID,
		Name:         name,
		Metadata:     registerReq.Metadata,
		Expires:      time.Now().Add(sdkLeaseTTL),
		TLS:          true,
		ReverseToken: registerReq.ReverseToken,
	}

	// Register with lease manager
	if !serv.GetLeaseManager().UpdateLease(lease) {
		writeAPIError(w, http.StatusConflict, "lease_rejected", "failed to register lease (name conflict or policy violation)")
		return
	}

	// Clear dropped state in case this is a re-registration after disconnect
	serv.GetReverseHub().ClearDropped(registerReq.LeaseID)

	sniName := types.BuildSNIName(name, serv.BaseHost)
	if sniName == "" {
		serv.GetLeaseManager().DeleteLease(registerReq.LeaseID)
		writeAPIError(w, http.StatusInternalServerError, "sni_name_invalid", "failed to build SNI route name")
		return
	}
	if err := serv.GetSNIRouter().RegisterRoute(sniName, registerReq.LeaseID, name); err != nil {
		// Keep lease and route state consistent on partial failure.
		serv.GetLeaseManager().DeleteLease(registerReq.LeaseID)
		writeAPIError(w, http.StatusInternalServerError, "sni_register_failed", fmt.Sprintf("failed to register SNI route: %v", err))
		return
	}

	log.Info().
		Str("lease_id", registerReq.LeaseID).
		Str("name", name).
		Bool("tls", true).
		Msg("[Registry] Lease registered")

	// Build public URL
	publicURL := types.ServicePublicURL(flagPortalURL, name)

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
	unregisterReq.LeaseID = normalizeLeaseID(unregisterReq.LeaseID)

	// Delete from lease manager
	if serv.GetLeaseManager().DeleteLease(unregisterReq.LeaseID) {
		log.Info().
			Str("lease_id", unregisterReq.LeaseID).
			Msg("[Registry] Lease unregistered")
	}
	serv.GetSNIRouter().UnregisterRouteByLeaseID(unregisterReq.LeaseID)
	serv.GetReverseHub().DropLease(unregisterReq.LeaseID)

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

	renewReq.LeaseID, renewReq.ReverseToken = normalizeLeaseCredentials(renewReq.LeaseID, renewReq.ReverseToken)
	if !r.validateLeaseCredentials(w, renewReq.LeaseID, renewReq.ReverseToken) {
		return
	}

	// Get existing lease
	entry, ok := lookupLeaseEntry(serv, renewReq.LeaseID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "lease_not_found", "lease not found")
		return
	}
	if !reverseTokenMatches(entry.Lease.ReverseToken, renewReq.ReverseToken) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "unauthorized lease renewal")
		return
	}

	// Update expiration
	entry.Lease.Expires = time.Now().Add(sdkLeaseTTL)
	if !serv.GetLeaseManager().UpdateLease(entry.Lease) {
		writeAPIError(w, http.StatusInternalServerError, "renew_failed", "failed to renew lease")
		return
	}

	// Transport is TLS reverse-connect only; keep SNI route refreshed on renew.
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
