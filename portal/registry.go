package portal

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/types"
)

// DefaultLeaseTTL defines the default lease lifetime across relay components.
const DefaultLeaseTTL = 30 * time.Second

// RegistryAdmissionInput describes runtime context for control-plane admission checks.
type RegistryAdmissionInput struct {
	RawLeaseID       string
	RawReverseToken  string
	ClientIP         string
	IsClientIPBanned bool
	RequireExisting  bool
}

// RegistryAdmissionResult returns normalized, validated admission context.
type RegistryAdmissionResult struct {
	Entry        *types.LeaseEntry
	LeaseID      string
	ReverseToken string
	ClientIP     string
}

// RegistryRegisterInput describes a lease registration request.
type RegistryRegisterInput struct {
	LeaseID      string
	ReverseToken string
	Name         string
	Metadata     *types.Metadata
	PortalURL    string
	TLS          bool
}

// AdmitControlPlane validates and normalizes control-plane credentials before SDK operations.
func (g *RelayServer) AdmitControlPlane(input RegistryAdmissionInput) (RegistryAdmissionResult, *types.APIError) {
	leaseID, reverseToken := normalizeRegistryCredentials(input.RawLeaseID, input.RawReverseToken)
	if err := validateRegistryCredentials(leaseID, reverseToken); err != nil {
		return RegistryAdmissionResult{}, err
	}

	if input.IsClientIPBanned {
		return RegistryAdmissionResult{}, registryAPIError(http.StatusForbidden, "ip_banned", "ip is banned")
	}

	if g == nil || g.leaseManager == nil {
		return RegistryAdmissionResult{}, registryAPIError(http.StatusInternalServerError, "registry_unavailable", "registry service unavailable")
	}

	entry, exists := g.leaseManager.GetLeaseByID(leaseID)
	if input.RequireExisting && !exists {
		return RegistryAdmissionResult{}, registryAPIError(http.StatusNotFound, "lease_not_found", "lease not found")
	}

	if exists && !matchLeaseToken(entry.Lease.ReverseToken, reverseToken) {
		return RegistryAdmissionResult{}, registryAPIError(http.StatusUnauthorized, "unauthorized", "unauthorized reverse connect")
	}

	return RegistryAdmissionResult{
		LeaseID:      leaseID,
		ReverseToken: reverseToken,
		ClientIP:     strings.TrimSpace(input.ClientIP),
		Entry:        entry,
	}, nil
}

// RegisterLease creates a new lease and associated SNI route.
func (g *RelayServer) RegisterLease(input RegistryRegisterInput) (types.RegisterResponse, *types.APIError) {
	if g == nil || g.leaseManager == nil || g.reverseHub == nil || g.sniRouter == nil {
		return types.RegisterResponse{}, registryAPIError(http.StatusInternalServerError, "registry_unavailable", "registry service unavailable")
	}

	name := strings.TrimSpace(input.Name)
	if !types.IsValidServiceName(name) {
		return types.RegisterResponse{}, registryAPIError(http.StatusBadRequest, "invalid_name", "name must be a DNS label (letters, digits, hyphen; no dots or underscores)")
	}
	if !input.TLS {
		return types.RegisterResponse{}, registryAPIError(http.StatusBadRequest, "tls_required", "tls must be enabled")
	}

	metadata := types.Metadata{}
	if input.Metadata != nil {
		metadata = *input.Metadata
	}

	lease := &types.Lease{
		ID:           input.LeaseID,
		Name:         name,
		Metadata:     metadata,
		Expires:      time.Now().Add(DefaultLeaseTTL),
		TLS:          true,
		ReverseToken: input.ReverseToken,
	}

	if !g.leaseManager.UpdateLease(lease) {
		return types.RegisterResponse{}, registryAPIError(http.StatusConflict, "lease_rejected", "failed to register lease (name conflict or policy violation)")
	}
	g.reverseHub.ClearDropped(input.LeaseID)

	sniName := types.BuildSNIName(name, g.BaseHost)
	if sniName == "" {
		g.leaseManager.DeleteLease(input.LeaseID)
		return types.RegisterResponse{}, registryAPIError(http.StatusInternalServerError, "sni_name_invalid", "failed to build SNI route name")
	}
	if err := g.sniRouter.RegisterRoute(sniName, input.LeaseID, name); err != nil {
		g.leaseManager.DeleteLease(input.LeaseID)
		return types.RegisterResponse{}, registryAPIError(http.StatusInternalServerError, "sni_register_failed", fmt.Sprintf("failed to register SNI route: %v", err))
	}

	log.Info().
		Str("lease_id", input.LeaseID).
		Str("name", name).
		Bool("tls", true).
		Msg("[Registry] Lease registered")

	return types.RegisterResponse{
		LeaseID:   input.LeaseID,
		PublicURL: types.ServicePublicURL(strings.TrimSpace(input.PortalURL), name),
		Success:   true,
	}, nil
}

// UnregisterLease removes lease state, route state, and reverse-connection state.
func (g *RelayServer) UnregisterLease(leaseID string) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" || g == nil {
		return
	}

	if g.leaseManager != nil && g.leaseManager.DeleteLease(leaseID) {
		log.Info().
			Str("lease_id", leaseID).
			Msg("[Registry] Lease unregistered")
	}
	if g.sniRouter != nil {
		g.sniRouter.UnregisterRouteByLeaseID(leaseID)
	}
	if g.reverseHub != nil {
		g.reverseHub.DropLease(leaseID)
	}
}

// RenewLease extends lease expiry and opportunistically refreshes SNI routing.
func (g *RelayServer) RenewLease(entry *types.LeaseEntry) *types.APIError {
	if entry == nil || entry.Lease == nil {
		return registryAPIError(http.StatusNotFound, "lease_not_found", "lease not found")
	}
	if g == nil || g.leaseManager == nil || g.sniRouter == nil {
		return registryAPIError(http.StatusInternalServerError, "registry_unavailable", "registry service unavailable")
	}

	entry.Lease.Expires = time.Now().Add(DefaultLeaseTTL)
	if !g.leaseManager.UpdateLease(entry.Lease) {
		return registryAPIError(http.StatusInternalServerError, "renew_failed", "failed to renew lease")
	}

	sniName := types.BuildSNIName(entry.Lease.Name, g.BaseHost)
	if sniName == "" {
		log.Warn().
			Str("lease_id", entry.Lease.ID).
			Str("name", entry.Lease.Name).
			Str("base_host", g.BaseHost).
			Msg("[Registry] Skipping SNI route refresh due to invalid SNI name")
		return nil
	}
	if err := g.sniRouter.RegisterRoute(sniName, entry.Lease.ID, entry.Lease.Name); err != nil {
		log.Warn().
			Err(err).
			Str("lease_id", entry.Lease.ID).
			Str("name", entry.Lease.Name).
			Msg("[Registry] Failed to refresh SNI route on renew")
	}
	return nil
}

// RegistryDomain returns the configured relay base domain.
func (g *RelayServer) RegistryDomain() (types.DomainResponse, *types.APIError) {
	if g == nil {
		return types.DomainResponse{}, registryAPIError(http.StatusServiceUnavailable, "base_domain_missing", "base domain not configured")
	}
	baseHost := strings.TrimSpace(g.BaseHost)
	if baseHost == "" {
		return types.DomainResponse{}, registryAPIError(http.StatusServiceUnavailable, "base_domain_missing", "base domain not configured")
	}
	return types.DomainResponse{
		Success:    true,
		BaseDomain: baseHost,
	}, nil
}

// HandleRegistryConnect admits reverse traffic into the reverse hub.
func (g *RelayServer) HandleRegistryConnect(conn net.Conn, admission RegistryAdmissionResult) {
	if g == nil || g.reverseHub == nil {
		if conn != nil {
			_ = conn.Close()
		}
		return
	}
	g.reverseHub.HandleConnect(conn, admission.LeaseID, admission.ReverseToken, admission.ClientIP)
}

// matchLeaseToken compares lease-bound values in constant time.
func matchLeaseToken(expected, provided string) bool {
	expected = strings.TrimSpace(expected)
	provided = strings.TrimSpace(provided)
	if expected == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

func normalizeRegistryCredentials(rawLeaseID, rawReverseToken string) (leaseID, reverseToken string) {
	return strings.TrimSpace(rawLeaseID), strings.TrimSpace(rawReverseToken)
}

func validateRegistryCredentials(leaseID, reverseToken string) *types.APIError {
	if leaseID == "" {
		return registryAPIError(http.StatusBadRequest, "missing_lease_id", "lease_id is required")
	}
	if reverseToken == "" {
		return registryAPIError(http.StatusBadRequest, "missing_reverse_token", "reverse_token is required")
	}
	return nil
}

func registryAPIError(statusCode int, code, message string) *types.APIError {
	return &types.APIError{
		StatusCode: statusCode,
		Code:       code,
		Message:    message,
	}
}
