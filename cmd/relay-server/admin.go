package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	"gosuda.org/portal/portal"
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
	case "/admin":
		a.handleAdminIndex(w)
	case "/admin/leases":
		writeJSON(w, http.StatusOK, convertLeaseEntriesToRows(a.server, true, a.frontend.portalURL))
	default:
		http.NotFound(w, r)
	}
}

func (a *Admin) handleAdminIndex(w http.ResponseWriter) {
	rows := convertLeaseEntriesToRows(a.server, true, a.frontend.portalURL)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html><html><body><h1>Portal Admin</h1><p>%d leases</p><p><a href="/admin/leases">JSON lease list</a></p></body></html>`, len(rows))
}

func (a *Admin) authorize(r *http.Request) bool {
	if a.secret == "" {
		return true
	}
	if subtleValueMatch(strings.TrimSpace(r.URL.Query().Get("key")), a.secret) {
		return true
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") && subtleValueMatch(strings.TrimSpace(auth[7:]), a.secret) {
		return true
	}
	if strings.HasPrefix(strings.ToLower(auth), "basic ") {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(auth[6:]))
		if err == nil {
			parts := strings.SplitN(string(raw), ":", 2)
			if len(parts) == 2 && subtleValueMatch(parts[1], a.secret) {
				return true
			}
		}
	}
	return false
}
