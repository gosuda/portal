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
	"gosuda.org/portal/portal/utils/cert"
	"gosuda.org/portal/sdk"
)

// SDKRegistry handles HTTP API for SDK lease registration
type SDKRegistry struct{}

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
	case "csr":
		r.handleCSR(w, req, serv)
	case "domain":
		r.handleDomain(w, req, serv)
	case "connect":
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
	entry, ok := serv.GetLeaseManager().GetLeaseByID(renewReq.LeaseID)
	if !ok {
		writeJSON(w, map[string]any{
			"success": false,
			"message": "lease not found",
		})
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(entry.Lease.ReverseToken)), []byte(strings.TrimSpace(renewReq.ReverseToken))) != 1 {
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

// handleCSR handles Certificate Signing Request submissions
// The tunnel client submits a CSR, and the relay issues a certificate via ACME DNS-01
func (r *SDKRegistry) handleCSR(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check if certificate manager is available
	if serv.GetCertManager() == nil {
		writeJSON(w, sdk.CSRResponse{
			Success: false,
			Message: "certificate issuance not configured on this relay",
		})
		return
	}

	var csrReq sdk.CSRRequest
	if err := json.NewDecoder(req.Body).Decode(&csrReq); err != nil {
		log.Error().Err(err).Msg("[Registry] Failed to decode CSR request")
		writeJSON(w, sdk.CSRResponse{
			Success: false,
			Message: "invalid request body",
		})
		return
	}

	// Validate request
	if csrReq.LeaseID == "" {
		writeJSON(w, sdk.CSRResponse{
			Success: false,
			Message: "lease_id is required",
		})
		return
	}
	if csrReq.ReverseToken == "" {
		writeJSON(w, sdk.CSRResponse{
			Success: false,
			Message: "reverse_token is required",
		})
		return
	}
	if len(csrReq.CSR) == 0 {
		writeJSON(w, sdk.CSRResponse{
			Success: false,
			Message: "csr is required",
		})
		return
	}

	// Authenticate via lease
	entry, ok := serv.GetLeaseManager().GetLeaseByID(csrReq.LeaseID)
	if !ok {
		writeJSON(w, sdk.CSRResponse{
			Success: false,
			Message: "lease not found",
		})
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(entry.Lease.ReverseToken)), []byte(strings.TrimSpace(csrReq.ReverseToken))) != 1 {
		writeJSON(w, sdk.CSRResponse{
			Success: false,
			Message: "unauthorized",
		})
		return
	}

	// Parse CSR to extract and validate domain
	csrDomain, err := cert.ParseCSRDomain(csrReq.CSR)
	if err != nil {
		writeJSON(w, sdk.CSRResponse{
			Success: false,
			Message: fmt.Sprintf("invalid CSR: %v", err),
		})
		return
	}

	// Validate domain matches lease name + base host
	expectedDomain := strings.ToLower(entry.Lease.Name) + "." + serv.BaseHost
	if strings.ToLower(csrDomain) != expectedDomain {
		writeJSON(w, sdk.CSRResponse{
			Success: false,
			Message: fmt.Sprintf("domain mismatch: expected %s", expectedDomain),
		})
		return
	}

	// Issue certificate
	certReq := &cert.CSRRequest{
		Domain: csrDomain,
		CSR:    csrReq.CSR,
	}

	cert, err := serv.GetCertManager().IssueCertificate(req.Context(), certReq)
	if err != nil {
		log.Error().Err(err).
			Str("lease_id", csrReq.LeaseID).
			Str("domain", csrDomain).
			Msg("[Registry] Failed to issue certificate")
		writeJSON(w, sdk.CSRResponse{
			Success: false,
			Message: fmt.Sprintf("certificate issuance failed: %v", err),
		})
		return
	}

	log.Info().
		Str("lease_id", csrReq.LeaseID).
		Str("domain", csrDomain).
		Time("expires", cert.ExpiresAt).
		Msg("[Registry] Certificate issued")

	writeJSON(w, sdk.CSRResponse{
		Success:     true,
		Certificate: cert.Certificate,
		ExpiresAt:   cert.ExpiresAt.Format(time.RFC3339),
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
