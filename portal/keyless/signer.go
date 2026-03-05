package keyless

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	ksigner "github.com/gosuda/keyless_tls/relay/signer"
	"github.com/gosuda/keyless_tls/relay/signrpc"
)

const (
	RelayKeyID         = "relay-cert"
	defaultAllowedSkew = 30 * time.Second
)

var (
	ErrSignerDisabled   = errors.New("keyless signer is disabled")
	ErrInvalidArgument  = ksigner.ErrInvalidArgument
	ErrPermissionDenied = ksigner.ErrPermissionDenied
)

type SignRequest = signrpc.SignRequest
type SignResponse = signrpc.SignResponse
type ErrorResponse = signrpc.ErrorResponse

// Signer serves the keyless signing endpoint used by tunnel keyless mode.
type Signer struct {
	service *ksigner.Service
	keyID   string
}

func NewSigner(KeyFile string) (*Signer, error) {
	keyPEM, err := os.ReadFile(KeyFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read keyless signing key: %w", err)
	}

	signingKey, err := ksigner.ParsePrivateKeyPEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse keyless signing key: %w", err)
	}

	store := ksigner.NewStaticKeyStore()
	if err := store.Put(RelayKeyID, signingKey); err != nil {
		return nil, fmt.Errorf("register keyless signing key: %w", err)
	}

	svc := &ksigner.Service{
		Store:       store,
		AllowedSkew: defaultAllowedSkew,
	}

	return &Signer{
		service: svc,
		keyID:   RelayKeyID,
	}, nil
}

func (s *Signer) KeyID() string {
	if s == nil {
		return ""
	}
	return s.keyID
}

func (s *Signer) Sign(ctx context.Context, req *SignRequest) (*SignResponse, error) {
	if s == nil || s.service == nil {
		return nil, ErrSignerDisabled
	}
	return s.service.Sign(ctx, req)
}
