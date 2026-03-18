package portal

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal/datagram"
	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

var (
	errLeaseNotFound    = errors.New(types.APIErrorCodeLeaseNotFound)
	errIPBanned         = errors.New(types.APIErrorCodeIPBanned)
	errUnauthorized     = errors.New(types.APIErrorCodeUnauthorized)
	errHostnameConflict = errors.New(types.APIErrorCodeHostnameConflict)
)

func (s *Server) newAPIServer(listener net.Listener, apiMux *http.ServeMux, apiTLS keyless.TLSMaterialConfig) (net.Listener, *http.Server, io.Closer, error) {
	keylessSignerHandler, err := newKeylessSignerHandler(apiTLS)
	if err != nil {
		return nil, nil, nil, err
	}

	apiServer := &http.Server{
		Handler:           s.apiHandler(apiMux, keylessSignerHandler),
		ReadHeaderTimeout: 10 * time.Second,
		TLSNextProto:      make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
	}

	apiCloser, err := keyless.AttachToHTTPServer(apiServer, apiTLS)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("configure api tls: %w", err)
	}

	return tls.NewListener(listener, apiServer.TLSConfig), apiServer, apiCloser, nil
}

func (s *Server) apiHandler(base *http.ServeMux, keylessSignerHandler http.Handler) http.Handler {
	if base == nil {
		base = http.NewServeMux()
		base.HandleFunc("/{$}", s.handleRoot)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimSpace(r.URL.Path) {
		case types.PathHealthz:
			s.handleHealthz(w, r)
		case types.PathSDKDomain:
			s.handleDomain(w, r)
		case types.PathSDKRegister:
			s.handleRegister(w, r)
		case types.PathSDKRenew:
			s.handleRenew(w, r)
		case types.PathSDKUnregister:
			s.handleUnregister(w, r)
		case types.PathSDKConnect:
			s.handleConnect(w, r)
		case types.PathV1Sign:
			if keylessSignerHandler == nil {
				http.NotFound(w, r)
				return
			}
			keylessSignerHandler.ServeHTTP(w, r)
		default:
			base.ServeHTTP(w, r)
		}
	})
}

func (s *Server) handleRoot(w http.ResponseWriter, _ *http.Request) {
	utils.WriteAPIData(w, http.StatusOK, map[string]any{
		"service": "portal-relay",
		"root":    s.rootHost,
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	utils.WriteAPIData(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleDomain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		utils.WriteAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}

	utils.WriteAPIData(w, http.StatusOK, types.DomainResponse{
		Version: types.SDKProtocolVersion,
	})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.WriteAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}

	clientIP := policy.ExtractClientIP(r, s.cfg.TrustProxyHeaders, s.cfg.TrustedProxyCIDRs)
	if s.registry.policy.IPFilter().IsIPBanned(clientIP) {
		utils.WriteAPIError(w, http.StatusForbidden, types.APIErrorCodeIPBanned, "request denied because source IP is banned")
		return
	}

	var req types.RegisterRequest
	if err := utils.DecodeJSONBody(w, r, &req, defaultControlBodyLimit); err != nil {
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidJSON, err.Error())
		return
	}

	resp, err := s.registerLease(req, clientIP)
	if err != nil {
		status, code := http.StatusBadRequest, types.APIErrorCodeInvalidRequest
		if errors.Is(err, errHostnameConflict) {
			status, code = http.StatusConflict, types.APIErrorCodeHostnameConflict
		}
		if errors.Is(err, errIPBanned) {
			status, code = http.StatusForbidden, types.APIErrorCodeIPBanned
		}
		if errors.Is(err, datagram.ErrPortExhausted) {
			status, code = http.StatusServiceUnavailable, types.APIErrorCodeUDPPortExhausted
		}
		utils.WriteAPIError(w, status, code, err.Error())
		return
	}

	utils.WriteAPIData(w, http.StatusCreated, resp)
}

func (s *Server) handleRenew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.WriteAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}

	clientIP := policy.ExtractClientIP(r, s.cfg.TrustProxyHeaders, s.cfg.TrustedProxyCIDRs)
	if s.registry.policy.IPFilter().IsIPBanned(clientIP) {
		utils.WriteAPIError(w, http.StatusForbidden, types.APIErrorCodeIPBanned, "request denied because source IP is banned")
		return
	}

	var req types.RenewRequest
	if err := utils.DecodeJSONBody(w, r, &req, defaultControlBodyLimit); err != nil {
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidJSON, err.Error())
		return
	}

	resp, err := s.renewLease(req, clientIP)
	if err != nil {
		status, code := http.StatusBadRequest, types.APIErrorCodeInvalidRequest
		if errors.Is(err, errLeaseNotFound) {
			status, code = http.StatusNotFound, types.APIErrorCodeLeaseNotFound
		}
		if errors.Is(err, errUnauthorized) {
			status, code = http.StatusForbidden, types.APIErrorCodeUnauthorized
		}
		if errors.Is(err, errIPBanned) {
			status, code = http.StatusForbidden, types.APIErrorCodeIPBanned
		}
		utils.WriteAPIError(w, status, code, err.Error())
		return
	}

	utils.WriteAPIData(w, http.StatusOK, resp)
}

func (s *Server) handleUnregister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		utils.WriteAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}

	var req types.UnregisterRequest
	if err := utils.DecodeJSONBody(w, r, &req, defaultControlBodyLimit); err != nil {
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidJSON, err.Error())
		return
	}

	if err := s.unregisterLease(req); err != nil {
		status, code := http.StatusBadRequest, types.APIErrorCodeInvalidRequest
		if errors.Is(err, errLeaseNotFound) {
			status, code = http.StatusNotFound, types.APIErrorCodeLeaseNotFound
		}
		if errors.Is(err, errUnauthorized) {
			status, code = http.StatusForbidden, types.APIErrorCodeUnauthorized
		}
		utils.WriteAPIError(w, status, code, err.Error())
		return
	}

	utils.WriteAPIOK(w, http.StatusOK)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		utils.WriteAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}
	if r.ProtoMajor != 1 {
		utils.WriteAPIError(w, http.StatusHTTPVersionNotSupported, types.APIErrorCodeHTTP11Only, "reverse connect requires HTTP/1.1")
		return
	}

	leaseID := strings.TrimSpace(r.URL.Query().Get("lease_id"))
	token := strings.TrimSpace(r.Header.Get(types.HeaderReverseToken))
	clientIP := policy.ExtractClientIP(r, s.cfg.TrustProxyHeaders, s.cfg.TrustedProxyCIDRs)
	if s.registry.policy.IPFilter().IsIPBanned(clientIP) {
		utils.WriteAPIError(w, http.StatusForbidden, types.APIErrorCodeIPBanned, "request denied because source IP is banned")
		return
	}

	lease, err := s.findLeaseByID(leaseID)
	if err != nil {
		utils.WriteAPIError(w, http.StatusNotFound, types.APIErrorCodeLeaseNotFound, err.Error())
		return
	}
	if !s.registry.policy.IsLeaseRoutable(lease.ID) {
		utils.WriteAPIError(w, http.StatusForbidden, types.APIErrorCodeLeaseRejected, "lease is not approved for routing")
		return
	}
	if authErr := s.authorizeLeaseToken(lease, token); authErr != nil {
		utils.WriteAPIError(w, http.StatusForbidden, types.APIErrorCodeUnauthorized, authErr.Error())
		return
	}
	if !lease.SupportsStream() {
		utils.WriteAPIError(w, http.StatusConflict, types.APIErrorCodeTransportMismatch, "lease does not support stream transport")
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		utils.WriteAPIError(w, http.StatusInternalServerError, types.APIErrorCodeHijackUnsupported, "hijacking is not supported")
		return
	}

	conn, rw, err := hijacker.Hijack()
	if err != nil {
		utils.WriteAPIError(w, http.StatusInternalServerError, types.APIErrorCodeHijackFailed, err.Error())
		return
	}

	if _, err := rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: keep-alive\r\n\r\n"); err != nil {
		_ = conn.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return
	}

	session := newReverseSession(conn, s.cfg.IdleKeepaliveInterval)
	streamBroker := lease.StreamBroker()
	if streamBroker == nil {
		_ = session.Close()
		return
	}

	if err := streamBroker.Offer(session); err != nil {
		log.Warn().
			Err(err).
			Str("component", "relay-server").
			Str("lease_id", lease.ID).
			Str("lease_name", lease.Name).
			Str("remote_addr", session.RemoteAddr()).
			Msg("sdk reverse rejected")
		_ = session.Close()
		return
	}

	s.registry.Touch(lease.ID, clientIP, time.Now())
	log.Info().
		Str("component", "relay-server").
		Str("lease_id", lease.ID).
		Str("lease_name", lease.Name).
		Str("remote_addr", session.RemoteAddr()).
		Int("ready", streamBroker.ReadyCount()).
		Msg("sdk reverse connected")
}

func (s *Server) registerLease(req types.RegisterRequest, clientIP string) (types.RegisterResponse, error) {
	name, err := utils.NormalizeDNSLabel(req.Name)
	if err != nil {
		return types.RegisterResponse{}, err
	}
	if strings.TrimSpace(req.ReverseToken) == "" {
		return types.RegisterResponse{}, errors.New("reverse token is required")
	}
	if s.registry.policy.IPFilter().IsIPBanned(clientIP) {
		return types.RegisterResponse{}, errIPBanned
	}
	hostname, err := utils.LeaseHostname(name, s.rootHost)
	if err != nil {
		return types.RegisterResponse{}, err
	}

	ttl := s.cfg.LeaseTTL
	if req.TTL > 0 {
		ttl = time.Duration(req.TTL) * time.Second
	}

	capabilities, err := types.ParseLeaseCapabilities(req.Transport)
	if err != nil {
		return types.RegisterResponse{}, err
	}
	transport := capabilities.Transport()

	leaseID := utils.RandomID("lease_")
	now := time.Now()
	expiresAt := now.Add(ttl)
	runtime, err := newLeaseRuntime(leaseRuntimeConfig{
		Capabilities:  capabilities,
		IdleInterval:  s.cfg.IdleKeepaliveInterval,
		LeaseID:       leaseID,
		LeaseName:     name,
		PortAllocator: s.ports,
		ReadyLimit:    s.cfg.ReadyQueueLimit,
	})
	if err != nil {
		return types.RegisterResponse{}, err
	}

	record := &leaseRecord{
		Lease: types.Lease{
			ID:          leaseID,
			Name:        name,
			Hostname:    hostname,
			Metadata:    req.Metadata,
			ExpiresAt:   expiresAt,
			FirstSeenAt: now,
			LastSeenAt:  now,
			ClientIP:    clientIP,
			Transport:   transport,
			UDPPort:     runtime.UDPPort(),
		},
		ReverseToken: req.ReverseToken,
		Runtime:      runtime,
	}

	if err := runtime.Start(); err != nil {
		runtime.Close(s.ports)
		return types.RegisterResponse{}, err
	}

	if err := s.registry.Register(record); err != nil {
		runtime.Close(s.ports)
		return types.RegisterResponse{}, err
	}

	resp := types.RegisterResponse{
		LeaseID:    leaseID,
		Hostname:   hostname,
		Metadata:   record.Metadata,
		ExpiresAt:  expiresAt,
		ConnectURL: strings.TrimRight(s.cfg.PortalURL, "/") + types.PathSDKConnect,
		Transport:  transport,
	}
	if record.SupportsDatagram() {
		resp.UDPAddr = fmt.Sprintf("%s:%d", s.rootHost, record.UDPPort())
		resp.QUICAddr = s.quicPublicAddr()
	}

	return resp, nil
}

func (s *Server) renewLease(req types.RenewRequest, clientIP string) (types.RenewResponse, error) {
	if s.registry.policy.IPFilter().IsIPBanned(clientIP) {
		return types.RenewResponse{}, errIPBanned
	}

	ttl := s.cfg.LeaseTTL
	if req.TTL > 0 {
		ttl = time.Duration(req.TTL) * time.Second
	}
	record, err := s.registry.Renew(req.LeaseID, req.ReverseToken, ttl, clientIP)
	if err != nil {
		return types.RenewResponse{}, err
	}

	return types.RenewResponse{LeaseID: record.ID, ExpiresAt: record.ExpiresAt}, nil
}

func (s *Server) unregisterLease(req types.UnregisterRequest) error {
	record, err := s.registry.Unregister(req.LeaseID, req.ReverseToken)
	if err != nil {
		return err
	}
	s.closeLease(record)
	return nil
}

func (s *Server) findLeaseByID(leaseID string) (*leaseRecord, error) {
	return s.registry.FindByID(leaseID)
}

func (s *Server) authorizeLeaseToken(record *leaseRecord, token string) error {
	if record == nil {
		return errLeaseNotFound
	}
	if !utils.TokenMatches(record.ReverseToken, token) {
		return errUnauthorized
	}
	return nil
}

func (s *Server) runAPIServer() error {
	err := s.apiServer.Serve(s.apiListener)
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func newKeylessSignerHandler(apiTLS keyless.TLSMaterialConfig) (http.Handler, error) {
	if len(apiTLS.KeyPEM) == 0 {
		return nil, nil
	}

	signer, err := keyless.NewSigner(apiTLS.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("configure api signer: %w", err)
	}
	return signer.Handler(), nil
}
