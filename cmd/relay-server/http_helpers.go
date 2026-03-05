package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal/keyless"
	"gosuda.org/portal/portal/policy"
	"gosuda.org/portal/types"
)

func isSecureRequestWithPolicy(r *http.Request, trustProxyHeaders bool) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	if !trustProxyHeaders || !policy.IsTrustedProxyRemoteAddr(r.RemoteAddr) {
		return false
	}
	if hasForwardedToken(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return hasForwardedToken(r.Header.Get("X-Forwarded-Ssl"), "on")
}

func hasForwardedToken(raw, target string) bool {
	for token := range strings.SplitSeq(raw, ",") {
		if strings.EqualFold(strings.TrimSpace(token), target) {
			return true
		}
	}
	return false
}

func isWebSocketUpgrade(req *http.Request) bool {
	if req == nil {
		return false
	}
	return hasForwardedToken(req.Header.Get("Upgrade"), "websocket")
}

// getContentType returns the MIME type for a file extension.
func getContentType(ext string) string {
	switch ext {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".wasm":
		return "application/wasm"
	case ".css":
		return "text/css"
	case ".mp4":
		return "video/mp4"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	default:
		return ""
	}
}

// setCORSHeaders sets permissive CORS headers for GET/OPTIONS and common headers.
func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Accept-Encoding")
}

func writeAPIData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(types.APIEnvelope{
		OK:   true,
		Data: data,
	}); err != nil {
		log.Error().Err(err).Msg("[HTTP] Failed to encode API success response")
	}
}

func writeAPIOK(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(types.APIEnvelope{OK: true}); err != nil {
		log.Error().Err(err).Msg("[HTTP] Failed to encode API success response")
	}
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeAPIErrorWithData(w, status, code, message, nil)
}

func writeAPIErrorWithData(w http.ResponseWriter, status int, code, message string, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(types.APIEnvelope{
		OK:   false,
		Data: data,
		Error: &types.APIError{
			Code:    code,
			Message: message,
		},
	}); err != nil {
		log.Error().Err(err).Msg("[HTTP] Failed to encode API error response")
	}
}

func writeSignError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(keyless.ErrorResponse{Error: message})
}

func decodeLeaseID(encoded string) (string, bool) {
	idBytes, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		idBytes, err = base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return "", false
		}
	}
	return string(idBytes), true
}
