package main

import "strings"

func parseURLs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func isRelayControlPlanePath(path string) bool {
	switch strings.TrimSpace(path) {
	case "/sdk/register", "/sdk/connect", "/sdk/renew", "/sdk/unregister", "/sdk/domain":
		return true
	}
	return strings.HasPrefix(path, "/sdk/")
}
