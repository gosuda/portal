package netutil

import "gosuda.org/portal/types"

func NormalizeServiceName(name string) (string, bool) {
	return types.NormalizeServiceName(name)
}

func IsSubdomain(domain, host string) bool {
	return types.IsSubdomain(domain, host)
}

func DefaultAppPattern(base string) string {
	return types.DefaultAppPattern(base)
}

func PortalHostPort(portalURL string) string {
	return types.PortalHostPort(portalURL)
}

func PortalRootHost(portalURL string) string {
	return types.PortalRootHost(portalURL)
}

func IsLocalhost(host string) bool {
	return types.IsLocalhost(host)
}

func DefaultBootstrapFrom(base string) string {
	return types.DefaultBootstrapFrom(base)
}

func ParseURLs(raw string) []string {
	return types.ParseURLs(raw)
}

func ParsePortNumber(raw string, fallback int) int {
	return types.ParsePortNumber(raw, fallback)
}

func LoopbackForwardAddr(listenAddr string) string {
	return types.LoopbackForwardAddr(listenAddr)
}

func IsValidLeaseName(name string) bool {
	return types.IsValidLeaseName(name)
}

func NormalizeRelayAPIURLs(bootstrapServers []string) ([]string, error) {
	return types.NormalizeRelayAPIURLs(bootstrapServers)
}

func NormalizeRelayAPIURL(relayURL string) (string, error) {
	return types.NormalizeRelayAPIURL(relayURL)
}

func NormalizeTargetAddr(targetAddr string) (string, error) {
	return types.NormalizeTargetAddr(targetAddr)
}
