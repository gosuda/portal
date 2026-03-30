package types

const (
	ReleaseVersion         = "v2.0.8"
	SDKProtocolVersion     = "3"
	PortalRelayRegistryURL = "https://raw.githubusercontent.com/gosuda/portal/main/registry.json"

	HeaderAccessToken = "X-Portal-Token"
	MarkerKeepalive   = byte(0x00)
	MarkerTLSStart    = byte(0x02)
)
