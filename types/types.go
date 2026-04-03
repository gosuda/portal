package types

const (
	ReleaseVersion         = "v2.1.2"
	ProtocolVersion        = "5"
	PortalRelayRegistryURL = "https://raw.githubusercontent.com/gosuda/portal/main/registry.json"

	HeaderAccessToken = "X-Portal-Access-Token"
	MarkerKeepalive   = byte(0x00)
	MarkerRawStart    = byte(0x01)
	MarkerTLSStart    = byte(0x02)
)
