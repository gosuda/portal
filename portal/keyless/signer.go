package keyless

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	ksigner "github.com/gosuda/keyless_tls/relay/signer"
	"github.com/gosuda/keyless_tls/relay/signrpc"
)

const (
	defaultKeyID       = "relay-cert"
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

type Config struct {
	KeyFile string
}

func NewSigner(cfg Config) (*Signer, error) {
	keyFile := strings.TrimSpace(cfg.KeyFile)
	if keyFile == "" {
		return nil, nil
	}

	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read KEYLESS_KEY_FILE: %w", err)
	}

	signingKey, err := ksigner.ParsePrivateKeyPEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse keyless signing key: %w", err)
	}

	store := ksigner.NewStaticKeyStore()
	if err := store.Put(defaultKeyID, signingKey); err != nil {
		return nil, fmt.Errorf("register keyless signing key: %w", err)
	}

	svc := &ksigner.Service{
		Store:       store,
		AllowedSkew: defaultAllowedSkew,
	}

	return &Signer{
		service: svc,
		keyID:   defaultKeyID,
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
