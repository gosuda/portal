package types

const (
	ReleaseVersion         = "v2.1.0"
	ProtocolVersion        = "4"
	PortalRelayRegistryURL = "https://raw.githubusercontent.com/gosuda/portal/main/registry.json"

	HeaderAccessToken = "X-Portal-Access-Token"
	MarkerKeepalive   = byte(0x00)
	MarkerTLSStart    = byte(0x02)
)
