package main

import (
	"crypto/subtle"
	"crypto/x509"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"gosuda.org/portal/portal"
)

const (
	controlPlaneCertCNPrefix = "lease:"
	controlPlaneLeaseURIPfx  = "spiffe://portal/lease/"
)

type admissionConfig struct {
	requireExistingLease bool
}

type admissionContext struct {
	entry    *portal.LeaseEntry
	clientIP string
	leaseID  string
	token    string
}

func constantLeaseMatch(expected, provided string) bool {
	expected = strings.TrimSpace(expected)
	provided = strings.TrimSpace(provided)
	if expected == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

func extractLeaseIDFromPeerCertificate(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	for _, uri := range cert.URIs {
		if uri == nil {
			continue
		}
		raw := strings.TrimSpace(uri.String())
		if after, ok := strings.CutPrefix(raw, controlPlaneLeaseURIPfx); ok {
			return after
		}
	}

	commonName := strings.TrimSpace(cert.Subject.CommonName)
	if after, ok := strings.CutPrefix(commonName, controlPlaneCertCNPrefix); ok {
		return after
	}
	return commonName
}

func validatePeerLeaseCertificate(req *http.Request, leaseID string) (string, string, bool) {
	if req == nil || req.TLS == nil || len(req.TLS.PeerCertificates) == 0 {
		return "client_cert_required", "client certificate is required", false
	}

	leaf := req.TLS.PeerCertificates[0]
	now := time.Now()
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return "client_cert_invalid", "client certificate is outside validity window", false
	}

	if len(leaf.ExtKeyUsage) > 0 {
		hasClientAuth := slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
		if !hasClientAuth {
			return "client_cert_invalid", "client certificate does not allow client authentication", false
		}
	}

	certLeaseID := strings.TrimSpace(extractLeaseIDFromPeerCertificate(leaf))
	if certLeaseID == "" {
		return "cert_lease_missing", "client certificate does not include lease identity", false
	}
	if !constantLeaseMatch(leaseID, certLeaseID) {
		return "cert_lease_mismatch", fmt.Sprintf("client certificate lease identity mismatch: requested=%s cert=%s", leaseID, certLeaseID), false
	}
	return "", "", true
}

func (r *SDKRegistry) admitControlPlane(w http.ResponseWriter, req *http.Request, serv *portal.RelayServer, rawLeaseID, rawToken string, cfg admissionConfig) (*admissionContext, bool) {
	leaseID, token := normalizeLeaseCredentials(rawLeaseID, rawToken)
	if !r.validateLeaseCredentials(w, leaseID, token) {
		return nil, false
	}

	clientIP := r.extractClientIP(req)
	if r.isClientIPBanned(clientIP) {
		writeAPIError(w, http.StatusForbidden, "ip_banned", "ip is banned")
		return nil, false
	}

	entry, exists := lookupLeaseEntry(serv, leaseID)
	if cfg.requireExistingLease && !exists {
		writeAPIError(w, http.StatusNotFound, "lease_not_found", "lease not found")
		return nil, false
	}

	if code, message, ok := validatePeerLeaseCertificate(req, leaseID); !ok {
		writeAPIError(w, http.StatusUnauthorized, code, message)
		return nil, false
	}

	if exists && !constantLeaseMatch(entry.Lease.ReverseToken, token) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "unauthorized reverse connect")
		return nil, false
	}

	return &admissionContext{
		clientIP: clientIP,
		leaseID:  leaseID,
		token:    token,
		entry:    entry,
	}, true
}
