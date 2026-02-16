package utils

import (
	"mime"
	"net"
	"net/http"
	"strings"
)

// IsHTMLContentType checks if the Content-Type header indicates HTML content
// It properly handles media type parsing with parameters like charset
func IsHTMLContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.HasPrefix(strings.ToLower(contentType), "text/html")
	}
	return mediaType == "text/html"
}

// GetContentType returns the MIME type for a file extension
func GetContentType(ext string) string {
	switch ext {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
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

// SetCORSHeaders sets permissive CORS headers for GET/OPTIONS and common headers
func SetCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Accept-Encoding")
}

func IsLocalhost(r *http.Request) bool {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}

	// If a proxy/adapter reports a hostname, allow Docker Desktop host alias.
	if strings.EqualFold(host, "host.docker.internal") {
		return true
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// Try resolving hostnames to IPs (best-effort).
		if addrs, err := net.LookupIP(host); err == nil {
			for _, a := range addrs {
				if a.IsLoopback() || a.IsPrivate() {
					return true
				}
			}
		}
		return false
	}

	return ip.IsLoopback() || ip.IsPrivate()
}
