package types

const (
	ReleaseVersion         = "v2.1.1"
	ProtocolVersion        = "4"
	PortalRelayRegistryURL = "https://raw.githubusercontent.com/gosuda/portal/main/registry.json"

	HeaderAccessToken = "X-Portal-Access-Token"
	MarkerKeepalive   = byte(0x00)
	MarkerTLSStart    = byte(0x02)
	MarkerRawTCPStart = byte(0x03)
)
