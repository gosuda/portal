package types

const (
	ReleaseVersion         = "v2.0.9"
	ProtocolVersion        = "3"
	PortalRelayRegistryURL = "https://raw.githubusercontent.com/gosuda/portal/main/registry.json"

	HeaderAccessToken = "X-Portal-Access-Token"
	MarkerKeepalive   = byte(0x00)
	MarkerTLSStart    = byte(0x02)
)
