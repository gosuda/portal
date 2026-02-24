package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"gosuda.org/portal/cmd/relay-server/manager"
	"gosuda.org/portal/portal"
	"gosuda.org/portal/portal/utils/sni"
	"gosuda.org/portal/utils"
)

// SDKRegistry handles HTTP API for SDK lease registration
// Used by both tunnel clients and native applications
type SDKRegistry struct {
	server    *portal.RelayServer
	sniRouter *sni.Router
	baseHost  string
}

// NewSDKRegistry creates a new SDK registry
func NewSDKRegistry(server *portal.RelayServer, sniRouter *sni.Router, appURL string) *SDKRegistry {
	baseHost := strings.ToLower(strings.TrimSpace(
		utils.StripPort(utils.StripWildCard(utils.StripScheme(appURL))),
	))
	return &SDKRegistry{
		server:    server,
		sniRouter: sniRouter,
		baseHost:  baseHost,
	}
}

// RegisterRequest represents an SDK lease registration request
type RegisterRequest struct {
	LeaseID      string          `json:"lease_id"`
	Name         string          `json:"name"`
	Address      string          `json:"address"` // Backend address for TCP connection
	Metadata     portal.Metadata `json:"metadata"`
	TLSEnabled   bool            `json:"tls_enabled"` // Whether the backend handles TLS termination
	ReverseToken string          `json:"reverse_token"`
}

// RegisterResponse represents an SDK lease registration response
type RegisterResponse struct {
	Success   bool   `json:"success"`
	Message   string `json:"message,omitempty"`
	LeaseID   string `json:"lease_id,omitempty"`
	PublicURL string `json:"public_url,omitempty"`
}

// HandleRegister handles SDK lease registration requests
func (r *SDKRegistry) HandleRegister(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var registerReq RegisterRequest
	if err := json.NewDecoder(req.Body).Decode(&registerReq); err != nil {
		log.Error().Err(err).Msg("[Registry] Failed to decode registration request")
		writeJSON(w, RegisterResponse{
			Success: false,
			Message: "invalid request body",
		})
		return
	}

	// Validate request
	if registerReq.LeaseID == "" {
		writeJSON(w, RegisterResponse{
			Success: false,
			Message: "lease_id is required",
		})
		return
	}

	if registerReq.Name == "" {
		writeJSON(w, RegisterResponse{
			Success: false,
			Message: "name is required",
		})
		return
	}

	if registerReq.Address == "" {
		writeJSON(w, RegisterResponse{
			Success: false,
			Message: "address is required",
		})
		return
	}
	if strings.TrimSpace(registerReq.ReverseToken) == "" {
		writeJSON(w, RegisterResponse{
			Success: false,
			Message: "reverse_token is required",
		})
		return
	}

	resolvedAddr, err := resolveLeaseAddress(req, registerReq.Address)
	if err != nil {
		writeJSON(w, RegisterResponse{
			Success: false,
			Message: err.Error(),
		})
		return
	}

	// Create lease
	lease := &portal.Lease{
		ID:           registerReq.LeaseID,
		Name:         registerReq.Name,
		Address:      resolvedAddr,
		Metadata:     registerReq.Metadata,
		Expires:      time.Now().Add(30 * time.Second),
		TLSEnabled:   registerReq.TLSEnabled,
		ReverseToken: strings.TrimSpace(registerReq.ReverseToken),
	}

	// Register with lease manager
	if !r.server.GetLeaseManager().UpdateLease(lease) {
		writeJSON(w, RegisterResponse{
			Success: false,
			Message: "failed to register lease (name conflict or policy violation)",
		})
		return
	}

	if err := r.registerSNIRoute(registerReq.LeaseID, registerReq.Name, resolvedAddr); err != nil {
		// Keep lease and route state consistent on partial failure.
		r.server.GetLeaseManager().DeleteLease(registerReq.LeaseID)
		writeJSON(w, RegisterResponse{
			Success: false,
			Message: fmt.Sprintf("failed to register SNI route: %v", err),
		})
		return
	}

	log.Info().
		Str("lease_id", registerReq.LeaseID).
		Str("name", registerReq.Name).
		Str("address", resolvedAddr).
		Str("address_advertised", registerReq.Address).
		Bool("tls_enabled", registerReq.TLSEnabled).
		Msg("[Registry] Lease registered")

	// Build public URL
	publicURL := ""
	if flagPortalAppURL != "" {
		publicURL = "https://" + registerReq.Name + "." + stripWildcard(flagPortalAppURL)
	}

	writeJSON(w, RegisterResponse{
		Success:   true,
		LeaseID:   registerReq.LeaseID,
		PublicURL: publicURL,
	})
}

// HandleUnregister handles SDK lease unregistration requests
func (r *SDKRegistry) HandleUnregister(w http.ResponseWriter, req *http.Request) {
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
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "invalid request body",
		})
		return
	}

	if unregisterReq.LeaseID == "" {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "lease_id is required",
		})
		return
	}

	// Delete from lease manager
	if r.server.GetLeaseManager().DeleteLease(unregisterReq.LeaseID) {
		log.Info().
			Str("lease_id", unregisterReq.LeaseID).
			Msg("[Registry] Lease unregistered")
	}
	r.unregisterSNIRoute(unregisterReq.LeaseID)
	r.server.GetReverseHub().DropLease(unregisterReq.LeaseID)

	writeJSON(w, map[string]interface{}{
		"success": true,
	})
}

// HandleRenew handles SDK lease renewal requests (keepalive)
func (r *SDKRegistry) HandleRenew(w http.ResponseWriter, req *http.Request) {
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
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "invalid request body",
		})
		return
	}

	if renewReq.LeaseID == "" {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "lease_id is required",
		})
		return
	}
	if strings.TrimSpace(renewReq.ReverseToken) == "" {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "reverse_token is required",
		})
		return
	}

	// Get existing lease
	entry, ok := r.server.GetLeaseManager().GetLeaseByID(renewReq.LeaseID)
	if !ok {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "lease not found",
		})
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(entry.Lease.ReverseToken)), []byte(strings.TrimSpace(renewReq.ReverseToken))) != 1 {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "unauthorized lease renewal",
		})
		return
	}

	// Update expiration
	entry.Lease.Expires = time.Now().Add(30 * time.Second)
	if !r.server.GetLeaseManager().UpdateLease(entry.Lease) {
		writeJSON(w, map[string]interface{}{
			"success": false,
			"message": "failed to renew lease",
		})
		return
	}

	// Re-register route if needed (e.g., router restarted while lease remained active).
	if err := r.registerSNIRoute(entry.Lease.ID, entry.Lease.Name, entry.Lease.Address); err != nil {
		log.Warn().
			Err(err).
			Str("lease_id", entry.Lease.ID).
			Str("name", entry.Lease.Name).
			Msg("[Registry] Failed to refresh SNI route on renew")
	}

	writeJSON(w, map[string]interface{}{
		"success": true,
	})
}

func (r *SDKRegistry) registerSNIRoute(leaseID, name, address string) error {
	if r.sniRouter == nil {
		return nil
	}
	if r.baseHost == "" {
		return fmt.Errorf("invalid app domain configuration")
	}
	sniName := strings.ToLower(strings.TrimSpace(name)) + "." + r.baseHost
	return r.sniRouter.RegisterRoute(sniName, address, leaseID, name)
}

func resolveLeaseAddress(req *http.Request, advertisedAddr string) (string, error) {
	advertisedAddr = strings.TrimSpace(advertisedAddr)
	host, port, err := net.SplitHostPort(advertisedAddr)
	if err != nil {
		return "", fmt.Errorf("invalid address: %q", advertisedAddr)
	}

	if isLoopbackOrLocalHost(host) {
		clientIP := strings.TrimSpace(manager.ExtractClientIP(req))
		if clientIP == "" {
			return "", fmt.Errorf("cannot resolve client IP for address: %q", advertisedAddr)
		}
		host = clientIP
	}

	return net.JoinHostPort(host, port), nil
}

func isLoopbackOrLocalHost(host string) bool {
	h := strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if h == "" || h == "localhost" {
		return true
	}

	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}

	return ip.IsLoopback() || ip.IsUnspecified()
}

func (r *SDKRegistry) unregisterSNIRoute(leaseID string) {
	if r.sniRouter == nil {
		return
	}
	r.sniRouter.UnregisterRouteByLeaseID(leaseID)
}

// stripWildcard removes the wildcard prefix from a domain
func stripWildcard(domain string) string {
	if len(domain) > 2 && domain[:2] == "*." {
		return domain[2:]
	}
	return domain
}
