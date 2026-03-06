package main

import (
	"mime"
	"strings"
)

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
