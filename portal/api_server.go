package portal

import (
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/types"
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
	writeAPIData(w, http.StatusOK, map[string]any{
		"service": "portal-relay",
		"root":    s.rootHost,
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeAPIData(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleDomain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}

	name := r.URL.Query().Get("name")
	writeAPIData(w, http.StatusOK, types.DomainResponse{
		RootHost:          s.rootHost,
		SuggestedHostname: suggestHostname(name, s.rootHost),
		Version:           types.SDKProtocolVersion,
	})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}

	clientIP := s.clientIPFromRequest(r)
	if s.isClientIPBanned(clientIP) {
		writeAPIError(w, http.StatusForbidden, types.APIErrorCodeIPBanned, "request denied because source IP is banned")
		return
	}

	var req types.RegisterRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidJSON, err.Error())
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
		writeAPIError(w, status, code, err.Error())
		return
	}

	writeAPIData(w, http.StatusCreated, resp)
}

func (s *Server) handleRenew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}

	clientIP := s.clientIPFromRequest(r)
	if s.isClientIPBanned(clientIP) {
		writeAPIError(w, http.StatusForbidden, types.APIErrorCodeIPBanned, "request denied because source IP is banned")
		return
	}

	var req types.RenewRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidJSON, err.Error())
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
		writeAPIError(w, status, code, err.Error())
		return
	}

	writeAPIData(w, http.StatusOK, resp)
}

func (s *Server) handleUnregister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}

	var req types.UnregisterRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidJSON, err.Error())
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
		writeAPIError(w, status, code, err.Error())
		return
	}

	writeAPIOK(w, http.StatusOK)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, types.APIErrorCodeMethodNotAllowed, "method not allowed")
		return
	}
	if r.ProtoMajor != 1 {
		writeAPIError(w, http.StatusHTTPVersionNotSupported, types.APIErrorCodeHTTP11Only, "reverse connect requires HTTP/1.1")
		return
	}

	leaseID := strings.TrimSpace(r.URL.Query().Get("lease_id"))
	token := strings.TrimSpace(r.Header.Get(types.HeaderReverseToken))
	clientIP := s.clientIPFromRequest(r)
	if s.isClientIPBanned(clientIP) {
		writeAPIError(w, http.StatusForbidden, types.APIErrorCodeIPBanned, "request denied because source IP is banned")
		return
	}

	lease, err := s.findLeaseByID(leaseID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, types.APIErrorCodeLeaseNotFound, err.Error())
		return
	}
	if !s.registry.IsRoutable(lease) {
		writeAPIError(w, http.StatusForbidden, types.APIErrorCodeLeaseRejected, "lease is not approved for routing")
		return
	}
	if authErr := s.authorizeLeaseToken(lease, token); authErr != nil {
		writeAPIError(w, http.StatusForbidden, types.APIErrorCodeUnauthorized, authErr.Error())
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, types.APIErrorCodeHijackUnsupported, "hijacking is not supported")
		return
	}

	conn, rw, err := hijacker.Hijack()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, types.APIErrorCodeHijackFailed, err.Error())
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
	if err := lease.Broker.Offer(session); err != nil {
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
		Int("ready", lease.Broker.ReadyCount()).
		Msg("sdk reverse connected")
}

func (s *Server) registerLease(req types.RegisterRequest, clientIP string) (types.RegisterResponse, error) {
	if strings.TrimSpace(req.Name) == "" {
		return types.RegisterResponse{}, errors.New("name is required")
	}
	if strings.TrimSpace(req.ReverseToken) == "" {
		return types.RegisterResponse{}, errors.New("reverse token is required")
	}
	if s.isClientIPBanned(clientIP) {
		return types.RegisterResponse{}, errIPBanned
	}

	hostnames := normalizeHostnames(req.Hostnames)
	if len(hostnames) == 0 {
		hostnames = []string{suggestHostname(req.Name, s.rootHost)}
	}

	ttl := s.cfg.LeaseTTL
	if req.TTL > 0 {
		ttl = time.Duration(req.TTL) * time.Second
	}

	leaseID := randomID("lease_")
	now := time.Now()
	expiresAt := now.Add(ttl)
	record := &leaseRecord{
		ID:           leaseID,
		Name:         strings.TrimSpace(req.Name),
		Hostnames:    hostnames,
		Metadata:     req.Metadata,
		ReverseToken: req.ReverseToken,
		ExpiresAt:    expiresAt,
		FirstSeenAt:  now,
		LastSeenAt:   now,
		ClientIP:     clientIP,
		Broker:       newLeaseBroker(leaseID, s.cfg.IdleKeepaliveInterval, s.cfg.ReadyQueueLimit),
	}

	if err := s.registry.Register(record); err != nil {
		return types.RegisterResponse{}, err
	}

	return types.RegisterResponse{
		LeaseID:    leaseID,
		Hostnames:  append([]string(nil), hostnames...),
		Metadata:   record.Metadata,
		ExpiresAt:  expiresAt,
		ConnectURL: s.connectURL(),
	}, nil
}

func (s *Server) renewLease(req types.RenewRequest, clientIP string) (types.RenewResponse, error) {
	if s.isClientIPBanned(clientIP) {
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
	record.Broker.Close()
	return nil
}

func (s *Server) findLeaseByID(leaseID string) (*leaseRecord, error) {
	return s.registry.FindByID(leaseID)
}

func (s *Server) authorizeLeaseToken(record *leaseRecord, token string) error {
	if record == nil {
		return errLeaseNotFound
	}
	if !tokenMatches(record.ReverseToken, token) {
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

func (s *Server) connectURL() string {
	base := strings.TrimRight(s.cfg.PortalURL, "/")
	return base + types.PathSDKConnect
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, defaultControlBodyLimit)
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}

func normalizeHostnames(hosts []string) []string {
	seen := make(map[string]struct{}, len(hosts))
	out := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = normalizeHostname(host)
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, host)
	}
	return out
}

func tokenMatches(expected, actual string) bool {
	if len(expected) == 0 || len(actual) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func writeAPIData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(types.APIEnvelope[any]{OK: true, Data: data})
}

func writeAPIOK(w http.ResponseWriter, status int) {
	writeAPIData(w, status, map[string]any{})
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(types.APIEnvelope[any]{
		OK:    false,
		Error: &types.APIError{Code: code, Message: message},
	})
}

func (s *Server) clientIPFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	return policy.ExtractClientIP(r, s.cfg.TrustProxyHeaders)
}

func (s *Server) isClientIPBanned(clientIP string) bool {
	return s.registry.IsClientIPBanned(clientIP)
}

func validateAPITLS(apiTLS keyless.TLSMaterialConfig) error {
	if len(apiTLS.CertPEM) == 0 {
		return errors.New("api tls certificate is required")
	}
	if len(apiTLS.KeyPEM) == 0 && apiTLS.Keyless == nil {
		return errors.New("api tls key or keyless signer is required")
	}
	return nil
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
