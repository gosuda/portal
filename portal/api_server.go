package portal

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/portal/keyless"
	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/portal/transport"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

var (
	errFeatureUnavailable  = errors.New(types.APIErrorCodeFeatureUnavailable)
	errHostnameConflict    = errors.New(types.APIErrorCodeHostnameConflict)
	errIPBanned            = errors.New(types.APIErrorCodeIPBanned)
	errLeaseNotFound       = errors.New(types.APIErrorCodeLeaseNotFound)
	errLeaseRejected       = errors.New(types.APIErrorCodeLeaseRejected)
	errTransportMismatch   = errors.New(types.APIErrorCodeTransportMismatch)
	errUnauthorized        = errors.New(types.APIErrorCodeUnauthorized)
	errUDPDisabled         = errors.New(types.APIErrorCodeUDPDisabled)
	errUDPCapacityExceeded = errors.New(types.APIErrorCodeUDPCapacityExceeded)
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
		if errors.Is(err, errFeatureUnavailable) {
			status, code = http.StatusServiceUnavailable, types.APIErrorCodeFeatureUnavailable
		}
		if errors.Is(err, errHostnameConflict) {
			status, code = http.StatusConflict, types.APIErrorCodeHostnameConflict
		}
		if errors.Is(err, errIPBanned) {
			status, code = http.StatusForbidden, types.APIErrorCodeIPBanned
		}
		if errors.Is(err, transport.ErrPortExhausted) {
			status, code = http.StatusServiceUnavailable, types.APIErrorCodeUDPPortExhausted
		}
		if errors.Is(err, errUDPDisabled) {
			status, code = http.StatusForbidden, types.APIErrorCodeUDPDisabled
		}
		if errors.Is(err, errUDPCapacityExceeded) {
			status, code = http.StatusServiceUnavailable, types.APIErrorCodeUDPCapacityExceeded
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

	lease, err := s.admitLeaseByID(leaseID, token, false)
	switch {
	case errors.Is(err, errLeaseNotFound):
		utils.WriteAPIError(w, http.StatusNotFound, types.APIErrorCodeLeaseNotFound, err.Error())
		return
	case errors.Is(err, errLeaseRejected):
		utils.WriteAPIError(w, http.StatusForbidden, types.APIErrorCodeLeaseRejected, "lease is not approved for routing")
		return
	case errors.Is(err, errUnauthorized):
		utils.WriteAPIError(w, http.StatusForbidden, types.APIErrorCodeUnauthorized, err.Error())
		return
	case errors.Is(err, errTransportMismatch):
		utils.WriteAPIError(w, http.StatusConflict, types.APIErrorCodeTransportMismatch, "lease does not support stream transport")
		return
	case err != nil:
		utils.WriteAPIError(w, http.StatusBadRequest, types.APIErrorCodeInvalidRequest, err.Error())
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

	stream := lease.stream
	if stream == nil {
		_ = conn.Close()
		return
	}

	remoteAddr := ""
	if conn.RemoteAddr() != nil {
		remoteAddr = conn.RemoteAddr().String()
	}
	if err := stream.OfferConn(conn); err != nil {
		log.Warn().
			Err(err).
			Str("component", "relay-server").
			Str("lease_id", lease.ID).
			Str("lease_name", lease.Name).
			Str("remote_addr", remoteAddr).
			Msg("sdk reverse rejected")
		return
	}

	s.registry.Touch(lease.ID, clientIP, time.Now())
	log.Info().
		Str("component", "relay-server").
		Str("lease_id", lease.ID).
		Str("lease_name", lease.Name).
		Str("remote_addr", remoteAddr).
		Int("ready", stream.ReadyCount()).
		Msg("sdk reverse connected")
}

func (s *Server) handleQUICTunnelConn(conn *quic.Conn) {
	stream, err := conn.AcceptStream(context.Background())
	if err != nil {
		_ = conn.CloseWithError(1, "stream accept failed")
		return
	}

	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Second))
	var msg types.QUICControlMessage
	if err := json.NewDecoder(io.LimitReader(stream, defaultControlBodyLimit)).Decode(&msg); err != nil {
		_ = conn.CloseWithError(1, "control read failed")
		return
	}
	_ = stream.SetReadDeadline(time.Time{})
	if strings.TrimSpace(msg.LeaseID) == "" || strings.TrimSpace(msg.ReverseToken) == "" {
		_ = json.NewEncoder(stream).Encode(types.QUICControlResponse{OK: false, Error: "invalid_control_message"})
		_ = conn.CloseWithError(1, "invalid control message")
		return
	}

	lease, err := s.admitLeaseByID(msg.LeaseID, msg.ReverseToken, true)
	switch {
	case errors.Is(err, errLeaseNotFound):
		_ = json.NewEncoder(stream).Encode(types.QUICControlResponse{OK: false, Error: types.APIErrorCodeLeaseNotFound})
		_ = conn.CloseWithError(1, "lease not found")
		return
	case errors.Is(err, errUnauthorized):
		_ = json.NewEncoder(stream).Encode(types.QUICControlResponse{OK: false, Error: types.APIErrorCodeUnauthorized})
		_ = conn.CloseWithError(1, "unauthorized")
		return
	case errors.Is(err, errLeaseRejected):
		_ = json.NewEncoder(stream).Encode(types.QUICControlResponse{OK: false, Error: types.APIErrorCodeLeaseRejected})
		_ = conn.CloseWithError(1, "lease rejected")
		return
	case errors.Is(err, errTransportMismatch):
		_ = json.NewEncoder(stream).Encode(types.QUICControlResponse{OK: false, Error: types.APIErrorCodeTransportMismatch})
		_ = conn.CloseWithError(1, "transport mismatch")
		return
	case err != nil:
		_ = json.NewEncoder(stream).Encode(types.QUICControlResponse{OK: false, Error: types.APIErrorCodeInvalidRequest})
		_ = conn.CloseWithError(1, "invalid control message")
		return
	}

	dg := lease.datagram
	if dg == nil {
		_ = json.NewEncoder(stream).Encode(types.QUICControlResponse{OK: false, Error: types.APIErrorCodeTransportMismatch})
		_ = conn.CloseWithError(1, "transport mismatch")
		return
	}
	if err := dg.Register(conn); err != nil {
		_ = json.NewEncoder(stream).Encode(types.QUICControlResponse{OK: false, Error: "broker_closed"})
		_ = conn.CloseWithError(1, "broker closed")
		return
	}

	_ = json.NewEncoder(stream).Encode(types.QUICControlResponse{OK: true})
	s.registry.Touch(lease.ID, conn.RemoteAddr().String(), time.Now())
	log.Info().
		Str("component", "quic-tunnel-listener").
		Str("lease_id", lease.ID).
		Str("lease_name", lease.Name).
		Str("remote_addr", conn.RemoteAddr().String()).
		Msg("quic tunnel connected")
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

	if err := s.requireDatagramPlane(req.UDPEnabled); err != nil {
		return types.RegisterResponse{}, err
	}

	if req.UDPEnabled {
		if !s.registry.policy.IsUDPEnabled() {
			return types.RegisterResponse{}, errUDPDisabled
		}
		if max := s.registry.policy.UDPMaxLeases(); max > 0 && s.registry.CountDatagramLeases() >= max {
			return types.RegisterResponse{}, errUDPCapacityExceeded
		}
	}

	leaseID := utils.RandomID("lease_")
	now := time.Now()
	expiresAt := now.Add(ttl)
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
			UDPEnabled:  req.UDPEnabled,
		},
		ReverseToken: req.ReverseToken,
		stream:       transport.NewRelayStream(leaseID, s.cfg.IdleKeepaliveInterval, s.cfg.ReadyQueueLimit),
	}
	if req.UDPEnabled {
		if s.ports == nil {
			return types.RegisterResponse{}, errors.New("udp port allocation not available")
		}
		port, err := s.ports.Allocate(name)
		if err != nil {
			return types.RegisterResponse{}, fmt.Errorf("allocate udp port: %w", err)
		}
		record.datagram = transport.NewRelayDatagram(leaseID, port)
		record.ports = s.ports
	}

	if err := record.Start(); err != nil {
		record.Close()
		return types.RegisterResponse{}, err
	}

	if err := s.registry.Register(record); err != nil {
		record.Close()
		return types.RegisterResponse{}, err
	}

	resp := types.RegisterResponse{
		LeaseID:    leaseID,
		Hostname:   hostname,
		Metadata:   record.Metadata,
		ExpiresAt:  expiresAt,
		UDPEnabled: record.UDPEnabled,
	}
	resp.ConnectURL = strings.TrimRight(s.cfg.PortalURL, "/") + types.PathSDKConnect
	if record.datagram != nil {
		resp.UDPAddr = fmt.Sprintf("%s:%d", s.rootHost, record.datagram.UDPPort())
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
