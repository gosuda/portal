package common

import (
	"errors"
	"time"
)

const (
	ProtocolMagic   = "P2"
	ProtocolVersion = 0x02
	ProtocolString  = "PORTAL/2"

	TypeDataKCP   = 0x01
	TypeProbeReq  = 0x02
	TypeProbeResp = 0x03
	TypeSAInit    = 0x10
	TypeSAResp    = 0x11
	TypeSAResume  = 0x12
	TypeCtrlData  = 0x20

	MsgRelayGroupInfoReq  = 0x01
	MsgRelayGroupInfoResp = 0x02
	MsgLeaseLookupReq     = 0x03
	MsgLeaseLookupResp    = 0x04
	MsgLeaseRegisterReq   = 0x05
	MsgLeaseRegisterResp  = 0x06
	MsgLeaseRefreshReq    = 0x07
	MsgLeaseRefreshResp   = 0x08
	MsgLeaseDeleteReq     = 0x09
	MsgLeaseDeleteResp    = 0x0A

	StatusOK              = 0x00
	StatusNotFound        = 0x01
	StatusConflict        = 0x02
	StatusUnresolved      = 0x03
	StatusUnauthorized    = 0x04
	StatusInvalidArgument = 0x05
	StatusRateLimited     = 0x06

	PeerQueryTimeout    = 250 * time.Millisecond
	RoutingEvalInterval = 1 * time.Second
	SwitchCooldown      = 5 * time.Second
	LossTimeoutBase     = 200 * time.Millisecond
	LossTimeoutMax      = 2 * time.Second

	LeaseTTL        = 30 * time.Second
	RefreshInterval = 20 * time.Second

	LatencyWindowSize = 16
	RoutingDeltaPct   = 15
	LossThreshold     = 0.20
	JitterAlpha       = 0.5

	SessionIDSize = 16
	GroupIDSize   = 32
	RelayIDSize   = 32
	SignatureSize = 64

	MaxPacketSize    = 1 << 20
	MinPacketSize    = 48
	JumboFrameMarker = 0xFFFFFFFF
)

const (
	FlagEncrypted = 1 << iota
	FlagKeyPhase
	FlagAckEli
	FlagHasExt
	FlagHasTime
)

const (
	ExtRouteLabel = 0x01
	ExtPathClass  = 0x02
	ExtECN        = 0x03
	ExtMetadata   = 0x04
)

const (
	PathClassDefault    = 0x00
	PathClassLowLatency = 0x01
	PathClassBulk       = 0x02
)

var (
	ErrInvalidMagic         = errors.New("invalid magic number")
	ErrInvalidVersion       = errors.New("invalid protocol version")
	ErrInvalidType          = errors.New("invalid packet type")
	ErrInvalidLength        = errors.New("invalid packet length")
	ErrInvalidHeaderLen     = errors.New("invalid header length (must be multiple of 4)")
	ErrInvalidSessionID     = errors.New("invalid session ID")
	ErrReservedNotZero      = errors.New("reserved field must be zero")
	ErrPayloadTooLarge      = errors.New("payload too large")
	ErrMissingEncryptedFlag = errors.New("DATA_KCP must have encrypted flag set")
	ErrUnknownExtension     = errors.New("unknown extension type")
	ErrPacketTooLarge       = errors.New("packet exceeds maximum size")
	ErrPacketTooSmall       = errors.New("packet below minimum size")
	ErrJumboFrameRejected   = errors.New("jumbo frames not supported")

	ErrInvalidCertificate = errors.New("invalid certificate")
	ErrInvalidSignature   = errors.New("invalid signature")
	ErrIDMismatch         = errors.New("certificate ID does not match derived ID")

	ErrSessionNotFound     = errors.New("session not found")
	ErrSessionExpired      = errors.New("session expired")
	ErrSessionInvalid      = errors.New("invalid session ticket")
	ErrSessionResumeFailed = errors.New("session resume failed")

	ErrLeaseNotFound     = errors.New("lease not found")
	ErrLeaseConflict     = errors.New("lease conflict")
	ErrLeaseExpired      = errors.New("lease expired")
	ErrLeaseUnauthorized = errors.New("unauthorized lease operation")

	ErrPeerTimeout      = errors.New("peer query timeout")
	ErrQuorumNotReached = errors.New("quorum not reached")
	ErrEquivocation     = errors.New("equivocation detected")
)
