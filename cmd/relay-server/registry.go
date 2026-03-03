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

// SDKRegistry handles HTTP API for SDK lease registration
type SDKRegistry struct{}

// HandleSDKRequest routes /sdk/* requests.
func (r *SDKRegistry) HandleSDKRequest(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
	path := strings.TrimSuffix(req.URL.Path, "/")

	switch path {
	case sdk.SDKPathRegister:
		r.handleRegister(w, req, serv)
	case sdk.SDKPathUnregister:
		r.handleUnregister(w, req, serv)
	case sdk.SDKPathRenew:
		r.handleRenew(w, req, serv)
	case sdk.SDKPathDomain:
		r.handleDomain(w, req, serv)
	case sdk.SDKPathConnect:
		r.handleConnect(w, req, serv)
	default:
		http.NotFound(w, req)
	}
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

	if registerReq.LeaseID == "" {
		writeJSON(w, sdk.RegisterResponse{
			Success: false,
			Message: "lease_id is required",
		})
		return
	}

	if registerReq.ReverseToken == "" {
		writeJSON(w, sdk.RegisterResponse{
			Success: false,
			Message: "reverse_token is required",
		})
		return
	}

	// Ownership semantics: re-registration of an existing lease ID requires the same reverse token.
	if entry, ok := serv.GetLeaseManager().GetLeaseByID(registerReq.LeaseID); ok && entry != nil && entry.Lease != nil {
		if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(entry.Lease.ReverseToken)), []byte(registerReq.ReverseToken)) != 1 {
			writeJSON(w, sdk.RegisterResponse{
				Success: false,
				Message: "unauthorized lease registration",
			})
			return
		}
	}

	// Create lease
	lease := &portal.Lease{
		ID:           registerReq.LeaseID,
		Name:         registerReq.Name,
		Metadata:     registerReq.Metadata,
		Expires:      time.Now().Add(30 * time.Second),
		TLS:          registerReq.TLS,
		ReverseToken: registerReq.ReverseToken,
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

	// Only register SNI route for TLS leases.
	if registerReq.TLS {
		sniName := strings.ToLower(strings.TrimSpace(registerReq.Name)) + "." + serv.BaseHost
		if err := serv.GetSNIRouter().RegisterRoute(sniName, registerReq.LeaseID, registerReq.Name); err != nil {
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
		Bool("tls", registerReq.TLS).
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

	var unregisterReq sdk.UnregisterRequest
	if err := json.NewDecoder(req.Body).Decode(&unregisterReq); err != nil {
		log.Error().Err(err).Msg("[Registry] Failed to decode unregistration request")
		writeJSON(w, sdk.APIResponse{
			Success: false,
			Message: "invalid request body",
		})
		return
	}

	// Delete from lease manager
	if serv.GetLeaseManager().DeleteLease(unregisterReq.LeaseID) {
		log.Info().
			Str("lease_id", unregisterReq.LeaseID).
			Msg("[Registry] Lease unregistered")
	}
	serv.GetSNIRouter().UnregisterRouteByLeaseID(unregisterReq.LeaseID)
	serv.GetReverseHub().DropLease(unregisterReq.LeaseID)

	writeJSON(w, sdk.APIResponse{
		Success: true,
	})
}

// handleRenew handles SDK lease renewal requests (keepalive)
func (r *SDKRegistry) handleRenew(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var renewReq sdk.RenewRequest
	if err := json.NewDecoder(req.Body).Decode(&renewReq); err != nil {
		log.Error().Err(err).Msg("[Registry] Failed to decode renewal request")
		writeJSON(w, sdk.APIResponse{
			Success: false,
			Message: "invalid request body",
		})
		return
	}

	if renewReq.ReverseToken == "" {
		writeJSON(w, sdk.RegisterResponse{
			Success: false,
			Message: "reverse_token is required",
		})
		return
	}

	// Get existing lease
	entry, ok := serv.GetLeaseManager().GetLeaseByID(renewReq.LeaseID)
	if !ok {
		writeJSON(w, sdk.APIResponse{
			Success: false,
			Message: "lease not found",
		})
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(entry.Lease.ReverseToken)), []byte(renewReq.ReverseToken)) != 1 {
		writeJSON(w, sdk.APIResponse{
			Success: false,
			Message: "unauthorized lease renewal",
		})
		return
	}

	// Update expiration
	entry.Lease.Expires = time.Now().Add(30 * time.Second)
	if !serv.GetLeaseManager().UpdateLease(entry.Lease) {
		writeJSON(w, sdk.APIResponse{
			Success: false,
			Message: "failed to renew lease",
		})
		return
	}

	// Re-register route if needed (e.g., router restarted while lease remained active).
	// Only TLS leases need SNI routes.
	if entry.Lease.TLS {
		sniName := strings.ToLower(strings.TrimSpace(entry.Lease.Name)) + "." + serv.BaseHost
		if err := serv.GetSNIRouter().RegisterRoute(sniName, entry.Lease.ID, entry.Lease.Name); err != nil {
			log.Warn().
				Err(err).
				Str("lease_id", entry.Lease.ID).
				Str("name", entry.Lease.Name).
				Msg("[Registry] Failed to refresh SNI route on renew")
		}
	}

	writeJSON(w, sdk.APIResponse{
		Success: true,
	})
}

// handleDomain returns the relay's base domain for TLS certificate construction.
func (r *SDKRegistry) handleDomain(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
	if serv.BaseHost == "" {
		writeJSON(w, map[string]any{
			"success": false,
			"message": "base domain not configured",
		})
		return
	}
	writeJSON(w, map[string]any{
		"success":     true,
		"base_domain": serv.BaseHost,
	})
}
