package sdk

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithCertHashSetsTLSVerificationCallback(t *testing.T) {
	t.Parallel()

	cfg := &ClientConfig{}
	hash := sha256.Sum256([]byte("relay-cert"))

	WithCertHash(hash[:])(cfg)

	require.NotNil(t, cfg.TLSConfig)
	require.True(t, cfg.TLSConfig.InsecureSkipVerify)
	require.NotNil(t, cfg.TLSConfig.VerifyPeerCertificate)
}

func TestWithCertHashMatchingCertHashSucceeds(t *testing.T) {
	t.Parallel()

	matchingCert := []byte("matching-cert")
	matchingHash := sha256.Sum256(matchingCert)

	cfg := &ClientConfig{}
	WithCertHash(matchingHash[:])(cfg)

	err := cfg.TLSConfig.VerifyPeerCertificate([][]byte{
		[]byte("non-matching-cert"),
		matchingCert,
	}, nil)
	require.NoError(t, err)
}

func TestWithCertHashMismatchingCertHashFails(t *testing.T) {
	t.Parallel()

	pinnedHash := sha256.Sum256([]byte("pinned-cert"))

	cfg := &ClientConfig{}
	WithCertHash(pinnedHash[:])(cfg)

	err := cfg.TLSConfig.VerifyPeerCertificate([][]byte{
		[]byte("cert-a"),
		[]byte("cert-b"),
	}, nil)
	require.EqualError(t, err, "portal: no certificate matches pinned hash")
}
