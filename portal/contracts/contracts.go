package contracts

import "gosuda.org/portal/types"

// API path constants for Portal relay server.
const (
	PathSDKPrefix     = types.PathSDKPrefix
	PathSDKRegister   = types.PathSDKRegister
	PathSDKUnregister = types.PathSDKUnregister
	PathSDKRenew      = types.PathSDKRenew
	PathSDKDomain     = types.PathSDKDomain
	PathSDKConnect    = types.PathSDKConnect

	PathAdminPrefix       = types.PathAdminPrefix
	PathAdminLogin        = types.PathAdminLogin
	PathAdminLogout       = types.PathAdminLogout
	PathAdminAuthStatus   = types.PathAdminAuthStatus
	PathAdminLeases       = types.PathAdminLeases
	PathAdminLeasesBanned = types.PathAdminLeasesBanned
	PathAdminStats        = types.PathAdminStats
	PathAdminSettings     = types.PathAdminSettings
	PathAdminApprovalMode = types.PathAdminApprovalMode

	PathKeylessSign = types.PathKeylessSign
	PathHealthz     = types.PathHealthz

	PathTunnelScript = types.PathTunnelScript
	PathTunnelBinary = types.PathTunnelBinary

	PathAppPrefix = types.PathAppPrefix
)

type (
	APIError       = types.APIError
	APIEnvelope    = types.APIEnvelope
	Metadata       = types.Metadata
	MetadataOption = types.MetadataOption
)

func WithDescription(description string) MetadataOption {
	return types.WithDescription(description)
}

func WithTags(tags []string) MetadataOption {
	return types.WithTags(tags)
}

func WithThumbnail(thumbnail string) MetadataOption {
	return types.WithThumbnail(thumbnail)
}

func WithOwner(owner string) MetadataOption {
	return types.WithOwner(owner)
}

func WithHide(hide bool) MetadataOption {
	return types.WithHide(hide)
}
