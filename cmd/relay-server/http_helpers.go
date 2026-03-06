package main

import (
	"crypto/subtle"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
)

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func hasPathPrefix(path, prefix string) bool {
	return strings.HasPrefix(strings.TrimSpace(path), prefix)
}

func trimPathPrefix(path, prefix string) string {
	return strings.TrimPrefix(strings.TrimSpace(path), prefix)
}

func subtleValueMatch(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func getContentType(ext string) string {
	ext = strings.TrimSpace(ext)
	if ext == "" {
		return ""
	}
	if contentType := mime.TypeByExtension(ext); contentType != "" {
		return contentType
	}

	switch strings.ToLower(ext) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".json", ".webmanifest":
		return "application/json; charset=utf-8"
	default:
		return ""
	}
}
