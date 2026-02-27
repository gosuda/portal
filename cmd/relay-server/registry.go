package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/api"
	"gosuda.org/portal/portal/utils/sni"
)

// funnelLeaseTTL is the default TTL for funnel leases.
const funnelLeaseTTL = 30 * time.Second

// Registry implements the REST API control plane for funnel lease management.
// It coordinates LeaseManager, SNI Router, and ReverseHub to keep state consistent.
type Registry struct {
	leaseManager *portal.LeaseManager
	sniRouter    *sni.Router
	reverseHub   *portal.ReverseHub
	ipManager    *manager.IPManager
	certManager  *CertManager
	funnelDomain string
}

// NewRegistry creates a new Registry with the given dependencies.
func NewRegistry(
	leaseManager *portal.LeaseManager,
	sniRouter *sni.Router,
	reverseHub *portal.ReverseHub,
	ipManager *manager.IPManager,
	certManager *CertManager,
	funnelDomain string,
) *Registry {
	return &Registry{
		leaseManager: leaseManager,
		sniRouter:    sniRouter,
		reverseHub:   reverseHub,
		ipManager:    ipManager,
		certManager:  certManager,
		funnelDomain: funnelDomain,
	}
}

// --- Handlers ---

// HandleRegister handles POST /api/register.
// Registers a funnel lease, sets up the SNI route, and returns credentials + cert.
func (reg *Registry) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req api.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, api.RegisterResponse{
			Success: false,
			Message: "invalid JSON body",
		})
		return
	}

	if req.Name == "" {
		writeJSONStatus(w, http.StatusBadRequest, api.RegisterResponse{
			Success: false,
			Message: "name is required",
		})
		return
	}

	// Validate name as a safe DNS subdomain label: lowercase alphanumeric + hyphens,
	// 1-63 chars, no leading/trailing hyphens. Prevents path traversal, SNI injection,
	// and URL manipulation.
	if !isValidSubdomainLabel(req.Name) {
		writeJSONStatus(w, http.StatusBadRequest, api.RegisterResponse{
			Success: false,
			Message: "name must be a valid DNS label (lowercase alphanumeric and hyphens, 1-63 chars)",
		})
		return
	}

	// Generate lease ID and reverse token
	leaseID, err := generateID(16)
	if err != nil {
		log.Error().Err(err).Msg("[Registry] failed to generate lease ID")
		writeJSONStatus(w, http.StatusInternalServerError, api.RegisterResponse{
			Success: false,
			Message: "internal error",
		})
		return
	}

	reverseToken, err := generateID(32)
	if err != nil {
		log.Error().Err(err).Msg("[Registry] failed to generate reverse token")
		writeJSONStatus(w, http.StatusInternalServerError, api.RegisterResponse{
			Success: false,
			Message: "internal error",
		})
		return
	}

	// Register lease (step 1)
	if !reg.leaseManager.UpdateLeaseSimple(leaseID, req.Name, reverseToken, funnelLeaseTTL, req.Metadata) {
		writeJSONStatus(w, http.StatusConflict, api.RegisterResponse{
			Success: false,
			Message: "name conflict or lease rejected",
		})
		return
	}

	// Associate lease with client IP for admin visibility
	if reg.ipManager != nil {
		reg.ipManager.RegisterLeaseIP(leaseID, manager.ExtractClientIP(r))
	}

	// Register SNI route (step 2) — use full subdomain so it matches the TLS SNI.
	reg.sniRouter.RegisterRoute(leaseID, req.Name+"."+reg.funnelDomain)

	publicURL := fmt.Sprintf("https://%s.%s", req.Name, reg.funnelDomain)

	log.Info().
		Str("lease_id", leaseID).
		Str("name", req.Name).
		Str("public_url", publicURL).
		Str("remote", r.RemoteAddr).
		Msg("[Registry] lease registered")

	// Provision TLS certificate for this subdomain (ACME if enabled, fallback otherwise).
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	certPEM, keyPEM, certErr := reg.certManager.GetCertPEM(ctx, req.Name)
	if certErr != nil {
		log.Error().Err(certErr).Str("name", req.Name).Msg("[Registry] certificate provisioning failed")
		// Clean up: lease was already created but is unusable without a cert.
		reg.leaseManager.DeleteLeaseByID(leaseID)
		writeJSONStatus(w, http.StatusServiceUnavailable, api.RegisterResponse{
			Success: false,
			Message: "certificate provisioning failed, try again later",
		})
		return
	}

	writeJSONStatus(w, http.StatusOK, api.RegisterResponse{
		Success:      true,
		LeaseID:      leaseID,
		ReverseToken: reverseToken,
		PublicURL:    publicURL,
		TLSCert:      string(certPEM),
		TLSKey:       string(keyPEM),
	})
}

// HandleRenew handles POST /api/renew.
// Extends the TTL of an existing funnel lease after token authentication.
func (reg *Registry) HandleRenew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req api.RenewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, api.RenewResponse{
			Success: false,
			Message: "invalid JSON body",
		})
		return
	}

	if req.LeaseID == "" || req.ReverseToken == "" {
		writeJSONStatus(w, http.StatusBadRequest, api.RenewResponse{
			Success: false,
			Message: "lease_id and reverse_token are required",
		})
		return
	}

	// Authenticate: look up lease and verify token
	entry, ok := reg.leaseManager.GetLeaseByID(req.LeaseID)
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, api.RenewResponse{
			Success: false,
			Message: "lease not found",
		})
		return
	}

	if subtle.ConstantTimeCompare([]byte(entry.ReverseToken), []byte(req.ReverseToken)) != 1 {
		log.Warn().Str("lease_id", req.LeaseID).Msg("[Registry] renew: unauthorized token")
		writeJSONStatus(w, http.StatusUnauthorized, api.RenewResponse{
			Success: false,
			Message: "unauthorized",
		})
		return
	}

	if !reg.leaseManager.RenewLeaseByID(req.LeaseID, funnelLeaseTTL) {
		writeJSONStatus(w, http.StatusNotFound, api.RenewResponse{
			Success: false,
			Message: "lease not found or banned",
		})
		return
	}

	// Update lease IP on renewal (tracks IP rotation)
	if reg.ipManager != nil {
		reg.ipManager.RegisterLeaseIP(req.LeaseID, manager.ExtractClientIP(r))
	}

	// Include latest cert in renewal response to enable cert rotation.
	// If ACME renewed the cert, the tunnel client picks it up here.
	resp := api.RenewResponse{Success: true}
	if entry.Lease != nil {
		certPEM, keyPEM, err := reg.certManager.GetCertPEM(r.Context(), entry.Lease.Name)
		if err == nil {
			resp.TLSCert = string(certPEM)
			resp.TLSKey = string(keyPEM)
		}
	}

	writeJSONStatus(w, http.StatusOK, resp)
}

// HandleUnregister handles POST /api/unregister.
// Removes the lease, SNI route, and drains reverse connections.
func (reg *Registry) HandleUnregister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req api.UnregisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, api.UnregisterResponse{
			Success: false,
			Message: "invalid JSON body",
		})
		return
	}

	if req.LeaseID == "" || req.ReverseToken == "" {
		writeJSONStatus(w, http.StatusBadRequest, api.UnregisterResponse{
			Success: false,
			Message: "lease_id and reverse_token are required",
		})
		return
	}

	// Authenticate
	entry, ok := reg.leaseManager.GetLeaseByID(req.LeaseID)
	if !ok {
		writeJSONStatus(w, http.StatusNotFound, api.UnregisterResponse{
			Success: false,
			Message: "lease not found",
		})
		return
	}

	if subtle.ConstantTimeCompare([]byte(entry.ReverseToken), []byte(req.ReverseToken)) != 1 {
		log.Warn().Str("lease_id", req.LeaseID).Msg("[Registry] unregister: unauthorized token")
		writeJSONStatus(w, http.StatusUnauthorized, api.UnregisterResponse{
			Success: false,
			Message: "unauthorized",
		})
		return
	}

	// Delete lease (callback will clean up SNI route + ReverseHub via onLeaseDeleted)
	reg.leaseManager.DeleteLeaseByID(req.LeaseID)

	log.Info().Str("lease_id", req.LeaseID).Msg("[Registry] lease unregistered")

	writeJSONStatus(w, http.StatusOK, api.UnregisterResponse{Success: true})
}

// HandleConnect handles GET /api/connect (WebSocket upgrade).
// Delegates to ReverseHub for reverse connection management.
func (reg *Registry) HandleConnect(w http.ResponseWriter, r *http.Request) {
	reg.reverseHub.HandleConnect(w, r)
}

// --- Helpers ---

// writeJSONStatus encodes v as JSON and writes it to w with the given status code.
func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error().Err(err).Msg("[Registry] failed to encode JSON response")
	}
}

// isValidSubdomainLabel checks that s is a valid DNS label: 1-63 lowercase
// alphanumeric characters and hyphens, not starting or ending with a hyphen.
var subdomainLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func isValidSubdomainLabel(s string) bool {
	return len(s) >= 1 && len(s) <= 63 && subdomainLabelRe.MatchString(s)
}

// generateID returns a hex-encoded random string of n bytes (2n hex chars).
func generateID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
