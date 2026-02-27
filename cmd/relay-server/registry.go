package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/net/websocket"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/sdk"
)

type RelayKeylessConfig struct {
	Enabled          bool
	DisabledReason   string
	CertChainPEM     []byte
	SignerEndpoint   string
	SignerServerName string
	KeyID            string
	RootCAPEM        []byte
	RequireMTLS      bool
	ClientCertPEM    []byte
	ClientKeyPEM     []byte
	signingKeyPEM    []byte
}

// SDKRegistry handles HTTP API for SDK lease registration.
type SDKRegistry struct {
	keylessConfig *RelayKeylessConfig
}

// HandleSDKRequest routes /sdk/* requests.
func (r *SDKRegistry) HandleSDKRequest(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
	route := strings.Trim(strings.TrimPrefix(req.URL.Path, "/sdk"), "/")

	switch route {
	case "register":
		r.handleRegister(w, req, serv)
	case "unregister":
		r.handleUnregister(w, req, serv)
	case "renew":
		r.handleRenew(w, req, serv)
	case "keyless/config":
		r.handleKeylessConfig(w, req, serv)
	case "connect":
		r.handleConnect(w, req, serv)
	default:
		http.NotFound(w, req)
	}
}

func (r *SDKRegistry) authenticateLease(leaseID, reverseToken string, serv *portal.RelayServer) (*portal.LeaseEntry, bool) {
	entry, ok := serv.GetLeaseManager().GetLeaseByID(leaseID)
	if !ok || entry == nil || entry.Lease == nil {
		return nil, false
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(entry.Lease.ReverseToken)), []byte(strings.TrimSpace(reverseToken))) != 1 {
		return nil, false
	}
	return entry, true
}

func (r *SDKRegistry) handleConnect(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
	wsHandler := websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler:   websocket.Handler(serv.GetReverseHub().HandleConnect),
	}
	wsHandler.ServeHTTP(w, req)
}

// handleRegister handles SDK lease registration requests
func (r *SDKRegistry) handleRegister(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var registerReq sdk.RegisterRequest
	if err := json.NewDecoder(req.Body).Decode(&registerReq); err != nil {
		log.Error().Err(err).Msg("[Registry] Failed to decode registration request")
		writeJSON(w, sdk.RegisterResponse{
			Success: false,
			Message: "invalid request body",
		})
		return
	}

	// Validate request
	if registerReq.LeaseID == "" {
		writeJSON(w, sdk.RegisterResponse{
			Success: false,
			Message: "lease_id is required",
		})
		return
	}

	if registerReq.Name == "" {
		writeJSON(w, sdk.RegisterResponse{
			Success: false,
			Message: "name is required",
		})
		return
	}

	if strings.TrimSpace(registerReq.ReverseToken) == "" {
		writeJSON(w, sdk.RegisterResponse{
			Success: false,
			Message: "reverse_token is required",
		})
		return
	}

	// Create lease
	lease := &portal.Lease{
		ID:           registerReq.LeaseID,
		Name:         registerReq.Name,
		Metadata:     registerReq.Metadata,
		Expires:      time.Now().Add(30 * time.Second),
		TLSEnabled:   registerReq.TLSEnabled,
		ReverseToken: strings.TrimSpace(registerReq.ReverseToken),
	}

	// Register with lease manager
	if !serv.GetLeaseManager().UpdateLease(lease) {
		writeJSON(w, sdk.RegisterResponse{
			Success: false,
			Message: "failed to register lease (name conflict or policy violation)",
		})
		return
	}

	// Clear dropped state in case this is a re-registration after disconnect
	serv.GetReverseHub().ClearDropped(registerReq.LeaseID)

	// Only register SNI route for TLS-enabled leases
	if registerReq.TLSEnabled {
		if err := registerSNIRoute(serv, registerReq.LeaseID, registerReq.Name); err != nil {
			// Keep lease and route state consistent on partial failure.
			serv.GetLeaseManager().DeleteLease(registerReq.LeaseID)
			writeJSON(w, sdk.RegisterResponse{
				Success: false,
				Message: fmt.Sprintf("failed to register SNI route: %v", err),
			})
			return
		}
	}

	log.Info().
		Str("lease_id", registerReq.LeaseID).
		Str("name", registerReq.Name).
		Bool("tls_enabled", registerReq.TLSEnabled).
		Msg("[Registry] Lease registered")

	// Build public URL
	publicURL := servicePublicURL(flagPortalURL, registerReq.Name)

	writeJSON(w, sdk.RegisterResponse{
		Success:   true,
		LeaseID:   registerReq.LeaseID,
		PublicURL: publicURL,
	})
}

// handleUnregister handles SDK lease unregistration requests
func (r *SDKRegistry) handleUnregister(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var unregisterReq struct {
		LeaseID string `json:"lease_id"`
	}

	if err := json.NewDecoder(req.Body).Decode(&unregisterReq); err != nil {
		log.Error().Err(err).Msg("[Registry] Failed to decode unregistration request")
		writeJSON(w, map[string]any{
			"success": false,
			"message": "invalid request body",
		})
		return
	}

	if unregisterReq.LeaseID == "" {
		writeJSON(w, map[string]any{
			"success": false,
			"message": "lease_id is required",
		})
		return
	}

	// Delete from lease manager
	if serv.GetLeaseManager().DeleteLease(unregisterReq.LeaseID) {
		log.Info().
			Str("lease_id", unregisterReq.LeaseID).
			Msg("[Registry] Lease unregistered")
	}
	unregisterSNIRoute(serv, unregisterReq.LeaseID)
	serv.GetReverseHub().DropLease(unregisterReq.LeaseID)

	writeJSON(w, map[string]any{
		"success": true,
	})
}

// handleRenew handles SDK lease renewal requests (keepalive)
func (r *SDKRegistry) handleRenew(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var renewReq struct {
		LeaseID      string `json:"lease_id"`
		ReverseToken string `json:"reverse_token"`
	}

	if err := json.NewDecoder(req.Body).Decode(&renewReq); err != nil {
		log.Error().Err(err).Msg("[Registry] Failed to decode renewal request")
		writeJSON(w, map[string]any{
			"success": false,
			"message": "invalid request body",
		})
		return
	}

	if renewReq.LeaseID == "" {
		writeJSON(w, map[string]any{
			"success": false,
			"message": "lease_id is required",
		})
		return
	}
	if strings.TrimSpace(renewReq.ReverseToken) == "" {
		writeJSON(w, map[string]any{
			"success": false,
			"message": "reverse_token is required",
		})
		return
	}

	// Get existing lease
	entry, ok := r.authenticateLease(renewReq.LeaseID, renewReq.ReverseToken, serv)
	if !ok {
		writeJSON(w, map[string]any{
			"success": false,
			"message": "unauthorized lease renewal",
		})
		return
	}

	// Update expiration
	entry.Lease.Expires = time.Now().Add(30 * time.Second)
	if !serv.GetLeaseManager().UpdateLease(entry.Lease) {
		writeJSON(w, map[string]any{
			"success": false,
			"message": "failed to renew lease",
		})
		return
	}

	// Re-register route if needed (e.g., router restarted while lease remained active).
	// Only TLS-enabled leases need SNI routes.
	if entry.Lease.TLSEnabled {
		if err := registerSNIRoute(serv, entry.Lease.ID, entry.Lease.Name); err != nil {
			log.Warn().
				Err(err).
				Str("lease_id", entry.Lease.ID).
				Str("name", entry.Lease.Name).
				Msg("[Registry] Failed to refresh SNI route on renew")
		}
	}

	writeJSON(w, map[string]any{
		"success": true,
	})
}

func registerSNIRoute(serv *portal.RelayServer, leaseID, name string) error {
	sniRouter := serv.GetSNIRouter()
	if sniRouter == nil {
		return nil
	}
	if serv.BaseHost == "" {
		return fmt.Errorf("base domain not configured (set PORTAL_URL)")
	}
	sniName := strings.ToLower(strings.TrimSpace(name)) + "." + serv.BaseHost
	return sniRouter.RegisterRoute(sniName, leaseID, name)
}

func unregisterSNIRoute(serv *portal.RelayServer, leaseID string) {
	sniRouter := serv.GetSNIRouter()
	if sniRouter == nil {
		return
	}
	sniRouter.UnregisterRouteByLeaseID(leaseID)
}

func (r *SDKRegistry) handleKeylessConfig(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.keylessConfig == nil || !r.keylessConfig.Enabled {
		reason := "keyless signer is not configured"
		if r.keylessConfig != nil && strings.TrimSpace(r.keylessConfig.DisabledReason) != "" {
			reason = r.keylessConfig.DisabledReason
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, sdk.KeylessConfigResponse{
			Success: false,
			Message: reason,
		})
		return
	}

	var configReq sdk.KeylessConfigRequest
	if err := json.NewDecoder(req.Body).Decode(&configReq); err != nil {
		log.Error().Err(err).Msg("[Registry] Failed to decode keyless config request")
		writeJSON(w, sdk.KeylessConfigResponse{
			Success: false,
			Message: "invalid request body",
		})
		return
	}

	if strings.TrimSpace(configReq.LeaseID) == "" {
		writeJSON(w, sdk.KeylessConfigResponse{
			Success: false,
			Message: "lease_id is required",
		})
		return
	}
	if strings.TrimSpace(configReq.ReverseToken) == "" {
		writeJSON(w, sdk.KeylessConfigResponse{
			Success: false,
			Message: "reverse_token is required",
		})
		return
	}

	entry, ok := r.authenticateLease(configReq.LeaseID, configReq.ReverseToken, serv)
	if !ok {
		writeJSON(w, sdk.KeylessConfigResponse{
			Success: false,
			Message: "unauthorized",
		})
		return
	}

	log.Info().
		Str("lease_id", configReq.LeaseID).
		Str("name", entry.Lease.Name).
		Bool("mtls", r.keylessConfig.RequireMTLS).
		Msg("[Registry] Keyless TLS config requested")

	writeJSON(w, sdk.KeylessConfigResponse{
		Success:          true,
		CertChainPEM:     r.keylessConfig.CertChainPEM,
		SignerEndpoint:   r.keylessConfig.SignerEndpoint,
		SignerServerName: r.keylessConfig.SignerServerName,
		KeyID:            r.keylessConfig.KeyID,
		RootCAPEM:        r.keylessConfig.RootCAPEM,
		RequireMTLS:      r.keylessConfig.RequireMTLS,
		ClientCertPEM:    r.keylessConfig.ClientCertPEM,
		ClientKeyPEM:     r.keylessConfig.ClientKeyPEM,
	})
}
