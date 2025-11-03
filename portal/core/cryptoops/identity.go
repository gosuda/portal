package cryptoops

import (
	"crypto/ed25519"
	"errors"

	"gosuda.org/portal/portal/core/proto/rdsec"
)

func ValidateIdentity(identity *rdsec.Identity) bool {
	if identity == nil {
		return false
	}

	if len(identity.PublicKey) != ed25519.PublicKeySize {
		return false
	}

	id := DeriveID(identity.PublicKey)

	return id == identity.Id
}

var (
	ErrInvalidMessage = errors.New("invalid message")
)

func VerifySignedPayload(message *rdsec.SignedPayload, id *rdsec.Identity) bool {
	if message == nil {
		return false
	}
	if id == nil {
		return false
	}

	if !ValidateIdentity(id) {
		return false
	}

	return ed25519.Verify(id.PublicKey, message.Data, message.Signature)
}
