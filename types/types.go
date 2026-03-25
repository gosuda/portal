package types

const (
	ReleaseVersion         = "v2.0.7"
	SDKProtocolVersion     = "2"
	PortalRelayRegistryURL = "https://raw.githubusercontent.com/gosuda/portal/main/registry.json"

	HeaderReverseToken = "X-Portal-Token"
	MarkerKeepalive    = byte(0x00)
	MarkerTLSStart     = byte(0x02)
)
