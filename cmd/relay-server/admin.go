package main

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"gosuda.org/portal/portal"
	"gosuda.org/portal/types"
)

type Admin struct {
	frontend   *Frontend
	server     *portal.Server
	secret     string
	trustProxy bool
}

func NewAdmin(secret string, trustProxy bool, frontend *Frontend) *Admin {
	return &Admin{
		secret:     strings.TrimSpace(secret),
		trustProxy: trustProxy,
		frontend:   frontend,
	}
}

func (a *Admin) Bind(server *portal.Server) {
	a.server = server
}

func (a *Admin) HandleAdminRequest(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="portal-admin"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch strings.TrimSuffix(r.URL.Path, "/") {
	case types.PathAdmin:
		a.handleAdminIndex(w)
	case types.PathAdminLeases:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(convertLeaseEntriesToRows(a.server, true, a.frontend.portalURL))
	default:
		http.NotFound(w, r)
	}
}

func (a *Admin) handleAdminIndex(w http.ResponseWriter) {
	rows := convertLeaseEntriesToRows(a.server, true, a.frontend.portalURL)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html><html><body><h1>Portal Admin</h1><p>%d leases</p><p><a href="%s">JSON lease list</a></p></body></html>`, len(rows), types.PathAdminLeases)
}

func (a *Admin) authorize(r *http.Request) bool {
	if a.secret == "" {
		return true
	}
	queryKey := strings.TrimSpace(r.URL.Query().Get("key"))
	if queryKey != "" && subtle.ConstantTimeCompare([]byte(queryKey), []byte(a.secret)) == 1 {
		return true
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		bearerToken := strings.TrimSpace(auth[7:])
		if bearerToken != "" && subtle.ConstantTimeCompare([]byte(bearerToken), []byte(a.secret)) == 1 {
			return true
		}
	}
	if strings.HasPrefix(strings.ToLower(auth), "basic ") {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(auth[6:]))
		if err == nil {
			parts := strings.SplitN(string(raw), ":", 2)
			if len(parts) == 2 && parts[1] != "" && subtle.ConstantTimeCompare([]byte(parts[1]), []byte(a.secret)) == 1 {
				return true
			}
		}
	}
	return false
}
