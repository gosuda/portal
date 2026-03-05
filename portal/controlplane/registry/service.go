package registry

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal/controlplane"
	"gosuda.org/portal/types"
)

// DefaultLeaseTTL defines the lease lifetime used by SDK register/renew flows.
const DefaultLeaseTTL = 30 * time.Second

// Backend provides the relay operations needed by the control-plane registry.
type Backend interface {
	BaseHost() string
	UpdateLease(lease *types.Lease) bool
	DeleteLease(leaseID string) bool
	GetLeaseByID(leaseID string) (*types.LeaseEntry, bool)
	ClearDropped(leaseID string)
	DropLease(leaseID string)
	RegisterRoute(sniName, leaseID, name string) error
	UnregisterRouteByLeaseID(leaseID string)
	HandleConnect(conn net.Conn, leaseID, token, clientIP string)
}

// Options configures service behavior.
type Options struct {
	Now      func() time.Time
	LeaseTTL time.Duration
}

// AdmissionInput describes runtime context for control-plane admission checks.
type AdmissionInput struct {
	ConnectionTLSState *tls.ConnectionState
	RawLeaseID         string
	RawReverseToken    string
	ClientIP           string
	IsClientIPBanned   bool
	RequireExisting    bool
}

// AdmissionResult returns normalized, validated admission context.
type AdmissionResult struct {
	Entry        *types.LeaseEntry
	LeaseID      string
	ReverseToken string
	ClientIP     string
}

// RegisterInput describes a lease registration request.
type RegisterInput struct {
	LeaseID      string
	ReverseToken string
	Name         string
	Metadata     *types.Metadata
	PortalURL    string
	TLS          bool
}

// Service encapsulates control-plane registry business logic.
type Service struct {
	backend  Backend
	now      func() time.Time
	leaseTTL time.Duration
}

// NewService constructs a control-plane registry service.
func NewService(backend Backend, opts Options) (*Service, error) {
	if backend == nil {
		return nil, errors.New("registry backend is required")
	}
	leaseTTL := opts.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = DefaultLeaseTTL
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		backend:  backend,
		leaseTTL: leaseTTL,
		now:      now,
	}, nil
}

// Admit validates and normalizes control-plane credentials before SDK operations.
func (s *Service) Admit(input AdmissionInput) (AdmissionResult, *types.APIError) {
	leaseID, reverseToken := normalizeLeaseCredentials(input.RawLeaseID, input.RawReverseToken)
	if err := validateLeaseCredentials(leaseID, reverseToken); err != nil {
		return AdmissionResult{}, err
	}

	if input.IsClientIPBanned {
		return AdmissionResult{}, apiError(httpStatusForbidden, "ip_banned", "ip is banned")
	}

	entry, exists := s.backend.GetLeaseByID(leaseID)
	if input.RequireExisting && !exists {
		return AdmissionResult{}, apiError(httpStatusNotFound, "lease_not_found", "lease not found")
	}

	if input.ConnectionTLSState != nil && len(input.ConnectionTLSState.PeerCertificates) > 0 {
		if code, message, ok := controlplane.ValidatePeerLeaseCertificate(input.ConnectionTLSState, leaseID); !ok {
			return AdmissionResult{}, apiError(httpStatusUnauthorized, code, message)
		}
	}

	if exists && !controlplane.MatchLeaseToken(entry.Lease.ReverseToken, reverseToken) {
		return AdmissionResult{}, apiError(httpStatusUnauthorized, "unauthorized", "unauthorized reverse connect")
	}

	return AdmissionResult{
		LeaseID:      leaseID,
		ReverseToken: reverseToken,
		ClientIP:     strings.TrimSpace(input.ClientIP),
		Entry:        entry,
	}, nil
}

// Register creates a new lease and associated SNI route.
func (s *Service) Register(input RegisterInput) (types.RegisterResponse, *types.APIError) {
	name := strings.TrimSpace(input.Name)
	if !types.IsValidLeaseName(name) {
		return types.RegisterResponse{}, apiError(httpStatusBadRequest, "invalid_name", "name must be a DNS label (letters, digits, hyphen; no dots or underscores)")
	}
	if !input.TLS {
		return types.RegisterResponse{}, apiError(httpStatusBadRequest, "tls_required", "tls must be enabled")
	}

	metadata := types.Metadata{}
	if input.Metadata != nil {
		metadata = *input.Metadata
	}

	lease := &types.Lease{
		ID:           input.LeaseID,
		Name:         name,
		Metadata:     metadata,
		Expires:      s.now().Add(s.leaseTTL),
		TLS:          true,
		ReverseToken: input.ReverseToken,
	}

	if !s.backend.UpdateLease(lease) {
		return types.RegisterResponse{}, apiError(httpStatusConflict, "lease_rejected", "failed to register lease (name conflict or policy violation)")
	}
	s.backend.ClearDropped(input.LeaseID)

	sniName := types.BuildSNIName(name, s.backend.BaseHost())
	if sniName == "" {
		s.backend.DeleteLease(input.LeaseID)
		return types.RegisterResponse{}, apiError(httpStatusInternalServerError, "sni_name_invalid", "failed to build SNI route name")
	}
	if err := s.backend.RegisterRoute(sniName, input.LeaseID, name); err != nil {
		s.backend.DeleteLease(input.LeaseID)
		return types.RegisterResponse{}, apiError(httpStatusInternalServerError, "sni_register_failed", fmt.Sprintf("failed to register SNI route: %v", err))
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

// Unregister removes lease state, route state, and reverse-connection state.
func (s *Service) Unregister(leaseID string) {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return
	}

	if s.backend.DeleteLease(leaseID) {
		log.Info().
			Str("lease_id", leaseID).
			Msg("[Registry] Lease unregistered")
	}
	s.backend.UnregisterRouteByLeaseID(leaseID)
	s.backend.DropLease(leaseID)
}

// Renew extends lease expiry and opportunistically refreshes SNI routing.
func (s *Service) Renew(entry *types.LeaseEntry) *types.APIError {
	if entry == nil || entry.Lease == nil {
		return apiError(httpStatusNotFound, "lease_not_found", "lease not found")
	}

	entry.Lease.Expires = s.now().Add(s.leaseTTL)
	if !s.backend.UpdateLease(entry.Lease) {
		return apiError(httpStatusInternalServerError, "renew_failed", "failed to renew lease")
	}

	sniName := types.BuildSNIName(entry.Lease.Name, s.backend.BaseHost())
	if sniName == "" {
		log.Warn().
			Str("lease_id", entry.Lease.ID).
			Str("name", entry.Lease.Name).
			Str("base_host", s.backend.BaseHost()).
			Msg("[Registry] Skipping SNI route refresh due to invalid SNI name")
		return nil
	}
	if err := s.backend.RegisterRoute(sniName, entry.Lease.ID, entry.Lease.Name); err != nil {
		log.Warn().
			Err(err).
			Str("lease_id", entry.Lease.ID).
			Str("name", entry.Lease.Name).
			Msg("[Registry] Failed to refresh SNI route on renew")
	}
	return nil
}

// Domain returns the configured relay base domain.
func (s *Service) Domain() (types.DomainResponse, *types.APIError) {
	baseHost := strings.TrimSpace(s.backend.BaseHost())
	if baseHost == "" {
		return types.DomainResponse{}, apiError(httpStatusServiceUnavailable, "base_domain_missing", "base domain not configured")
	}
	return types.DomainResponse{
		Success:    true,
		BaseDomain: baseHost,
	}, nil
}

// HandleConnect admits reverse traffic into the reverse hub.
func (s *Service) HandleConnect(conn net.Conn, admission AdmissionResult) {
	s.backend.HandleConnect(conn, admission.LeaseID, admission.ReverseToken, admission.ClientIP)
}

func normalizeLeaseCredentials(rawLeaseID, rawReverseToken string) (leaseID, reverseToken string) {
	return strings.TrimSpace(rawLeaseID), strings.TrimSpace(rawReverseToken)
}

func validateLeaseCredentials(leaseID, reverseToken string) *types.APIError {
	if leaseID == "" {
		return apiError(httpStatusBadRequest, "missing_lease_id", "lease_id is required")
	}
	if reverseToken == "" {
		return apiError(httpStatusBadRequest, "missing_reverse_token", "reverse_token is required")
	}
	return nil
}

func apiError(statusCode int, code, message string) *types.APIError {
	return &types.APIError{
		StatusCode: statusCode,
		Code:       code,
		Message:    message,
	}
}

// Local status code constants avoid pulling net/http into this package API.
const (
	httpStatusBadRequest          = 400
	httpStatusUnauthorized        = 401
	httpStatusForbidden           = 403
	httpStatusNotFound            = 404
	httpStatusConflict            = 409
	httpStatusInternalServerError = 500
	httpStatusServiceUnavailable  = 503
)
