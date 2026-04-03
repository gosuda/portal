package auth

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	secp256k1ecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spruceid/siwe-go"

	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

const (
	registerStatement        = "Register a portal lease"
	leaseAccessTokenAudience = "portal-sdk"
)

var (
	ErrChallengeExpired  = errors.New("register challenge expired")
	ErrChallengeNotFound = errors.New("register challenge not found")
	ErrInvalidSignature  = errors.New("siwe signature is invalid")
	ErrMessageMismatch   = errors.New("siwe message does not match register challenge")
)

const leaseTokenAlgorithm = jose.SignatureAlgorithm("ES256K")

type LeaseAccessTokenClaims struct {
	jwt.Claims
	Identity types.Identity `json:"identity"`
}

type es256kOpaqueSigner struct {
	keyID      string
	privateKey *secp256k1.PrivateKey
}

func (s *es256kOpaqueSigner) Public() *jose.JSONWebKey {
	return &jose.JSONWebKey{KeyID: s.keyID}
}

func (s *es256kOpaqueSigner) Algs() []jose.SignatureAlgorithm {
	return []jose.SignatureAlgorithm{leaseTokenAlgorithm}
}

func (s *es256kOpaqueSigner) SignPayload(payload []byte, alg jose.SignatureAlgorithm) ([]byte, error) {
	if alg != leaseTokenAlgorithm {
		return nil, jose.ErrUnsupportedAlgorithm
	}
	if s == nil || s.privateKey == nil {
		return nil, errors.New("signing key is required")
	}

	hash := sha256.Sum256(payload)
	compact := secp256k1ecdsa.SignCompact(s.privateKey, hash[:], false)
	if len(compact) != 65 {
		return nil, errors.New("invalid compact signature length")
	}

	signature := make([]byte, 64)
	copy(signature[:32], compact[1:33])
	copy(signature[32:], compact[33:65])
	return signature, nil
}

type es256kOpaqueVerifier struct {
	publicKey *secp256k1.PublicKey
}

func (v *es256kOpaqueVerifier) VerifyPayload(payload []byte, signature []byte, alg jose.SignatureAlgorithm) error {
	if alg != leaseTokenAlgorithm {
		return jose.ErrUnsupportedAlgorithm
	}
	if v == nil || v.publicKey == nil {
		return errors.New("verification key is required")
	}
	if len(signature) != 64 {
		return errors.New("invalid es256k signature length")
	}

	var r, s secp256k1.ModNScalar
	if overflow := r.SetByteSlice(signature[:32]); overflow || r.IsZero() {
		return errors.New("invalid es256k signature r")
	}
	if overflow := s.SetByteSlice(signature[32:]); overflow || s.IsZero() {
		return errors.New("invalid es256k signature s")
	}

	hash := sha256.Sum256(payload)
	return verifyRawSignature(hash[:], &r, &s, v.publicKey)
}

func verifyRawSignature(hash []byte, r, s *secp256k1.ModNScalar, publicKey *secp256k1.PublicKey) error {
	signature := secp256k1ecdsa.NewSignature(r, s)
	if !signature.Verify(hash, publicKey) {
		return errors.New("token signature is invalid")
	}
	return nil
}

type RegisterChallenge struct {
	ChallengeID string
	ExpiresAt   time.Time
	Request     types.RegisterChallengeRequest
	SIWEMessage string

	domain string
	nonce  string
}

func NewRegisterChallenge(req types.RegisterChallengeRequest, domain, uri string, now time.Time, ttl time.Duration) (*RegisterChallenge, error) {
	normalizedIdentity, err := utils.NormalizeIdentity(req.Identity)
	if err != nil {
		return nil, err
	}

	challengeID := utils.RandomID("rch_")
	nonce := siwe.GenerateNonce()
	expiresAt := now.UTC().Add(ttl)
	siweMessage, err := BuildRegisterChallengeMessage(domain, normalizedIdentity.Address, uri, challengeID, nonce, now.UTC(), expiresAt)
	if err != nil {
		return nil, err
	}

	normalizedRequest := types.RegisterChallengeRequest{
		Identity:   normalizedIdentity,
		Metadata:   req.Metadata.Copy(),
		TTL:        req.TTL,
		UDPEnabled: req.UDPEnabled,
		TCPEnabled: req.TCPEnabled,
	}

	return &RegisterChallenge{
		ChallengeID: challengeID,
		ExpiresAt:   expiresAt,
		Request:     normalizedRequest,
		SIWEMessage: siweMessage,
		domain:      strings.TrimSpace(domain),
		nonce:       nonce,
	}, nil
}

func BuildRegisterChallengeMessage(domain, address, uri, challengeID, nonce string, issuedAt, expiresAt time.Time) (string, error) {
	message, err := siwe.InitMessage(domain, address, uri, nonce, map[string]interface{}{
		"statement":      registerStatement,
		"chainId":        1,
		"issuedAt":       issuedAt.UTC().Format(time.RFC3339),
		"expirationTime": expiresAt.UTC().Format(time.RFC3339),
		"requestId":      challengeID,
	})
	if err != nil {
		return "", fmt.Errorf("build siwe message: %w", err)
	}
	return message.String(), nil
}

func (c *RegisterChallenge) Expired(now time.Time) bool {
	if c == nil {
		return true
	}
	return now.UTC().After(c.ExpiresAt)
}

func (c *RegisterChallenge) Verify(req types.RegisterRequest, now time.Time) error {
	if c == nil {
		return ErrChallengeNotFound
	}
	if strings.TrimSpace(req.SIWEMessage) != c.SIWEMessage {
		return ErrMessageMismatch
	}
	if err := VerifyRegisterChallengeMessage(c.SIWEMessage, req.SIWESignature, c.domain, c.nonce, now.UTC()); err != nil {
		return ErrInvalidSignature
	}
	return nil
}

func VerifyRegisterChallengeMessage(messageText, signature, domain, nonce string, now time.Time) error {
	message, err := siwe.ParseMessage(strings.TrimSpace(messageText))
	if err != nil {
		return err
	}
	normalizedDomain := strings.TrimSpace(domain)
	normalizedNonce := strings.TrimSpace(nonce)
	verifiedAt := now.UTC()
	_, err = message.Verify(strings.TrimSpace(signature), &normalizedDomain, &normalizedNonce, &verifiedAt)
	return err
}

func IssueLeaseAccessToken(privateKeyHex, keyID, issuer string, identity types.Identity, ttl time.Duration) (string, LeaseAccessTokenClaims, error) {
	privateKey, _, err := utils.ParseSecp256k1PrivateKeyHex(privateKeyHex, false)
	if err != nil {
		return "", LeaseAccessTokenClaims{}, err
	}
	normalizedIdentity, err := utils.NormalizeIdentity(identity)
	if err != nil {
		return "", LeaseAccessTokenClaims{}, err
	}

	signer, err := jose.NewSigner(jose.SigningKey{
		Algorithm: leaseTokenAlgorithm,
		Key: &es256kOpaqueSigner{
			keyID:      strings.TrimSpace(keyID),
			privateKey: privateKey,
		},
	}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", LeaseAccessTokenClaims{}, err
	}

	now := time.Now().UTC()
	expiresAt := now.Add(ttl)
	claims := LeaseAccessTokenClaims{
		Claims: jwt.Claims{
			Issuer:    strings.TrimSpace(issuer),
			Subject:   normalizedIdentity.Key(),
			Audience:  jwt.Audience{leaseAccessTokenAudience},
			ID:        utils.RandomID("tok_"),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Expiry:    jwt.NewNumericDate(expiresAt),
		},
		Identity: normalizedIdentity,
	}

	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		return "", LeaseAccessTokenClaims{}, err
	}
	return token, claims, nil
}

func VerifyLeaseAccessToken(token, publicKeyHex, issuer string, now time.Time) (LeaseAccessTokenClaims, error) {
	publicKey, err := utils.ParseSecp256k1PublicKeyHex(publicKeyHex)
	if err != nil {
		return LeaseAccessTokenClaims{}, err
	}

	parsed, err := jwt.ParseSigned(strings.TrimSpace(token), []jose.SignatureAlgorithm{leaseTokenAlgorithm})
	if err != nil {
		return LeaseAccessTokenClaims{}, err
	}

	var claims LeaseAccessTokenClaims
	if err := parsed.Claims(&es256kOpaqueVerifier{publicKey: publicKey}, &claims); err != nil {
		return LeaseAccessTokenClaims{}, err
	}
	normalizedClaimsIdentity, err := utils.NormalizeIdentity(claims.Identity)
	if err != nil {
		return LeaseAccessTokenClaims{}, err
	}
	if normalizedClaimsIdentity.Key() != claims.Subject {
		return LeaseAccessTokenClaims{}, errors.New("lease access token identity does not match subject")
	}
	claims.Identity = normalizedClaimsIdentity
	if err := claims.ValidateWithLeeway(jwt.Expected{
		Issuer:      strings.TrimSpace(issuer),
		AnyAudience: jwt.Audience{leaseAccessTokenAudience},
		Time:        now.UTC(),
	}, 0); err != nil {
		return LeaseAccessTokenClaims{}, err
	}
	return claims, nil
}
