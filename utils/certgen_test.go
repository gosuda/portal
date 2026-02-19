package utils

import (
	"crypto/sha256"
	"crypto/x509"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGenerateSelfSignedCert_BasicSuccess(t *testing.T) {
	tlsCert, hash, err := GenerateSelfSignedCert()
	require.NoError(t, err, "GenerateSelfSignedCert() error")

	require.NotEmpty(t, tlsCert.Certificate, "expected non-empty tls.Certificate DER chain")
	require.NotEmpty(t, tlsCert.Certificate[0], "expected non-empty tls.Certificate DER chain")
	require.Len(t, hash, sha256.Size, "hash length")
}

func TestGenerateSelfSignedCert_HashMatchesDER(t *testing.T) {
	tlsCert, hash, err := GenerateSelfSignedCert()
	require.NoError(t, err, "GenerateSelfSignedCert() error")

	require.NotEmpty(t, tlsCert.Certificate, "expected certificate chain to be non-empty")

	sum := sha256.Sum256(tlsCert.Certificate[0])
	require.Equal(t, sum[:], hash, "hash mismatch")
}

func TestGenerateSelfSignedCert_X509Properties(t *testing.T) {
	tlsCert, _, err := GenerateSelfSignedCert()
	require.NoError(t, err, "GenerateSelfSignedCert() error")

	require.NotEmpty(t, tlsCert.Certificate, "expected certificate chain to be non-empty")

	cert, err := x509.ParseCertificate(tlsCert.Certificate[0])
	require.NoError(t, err, "x509.ParseCertificate() error")

	require.Equal(t, "portal-dev", cert.Subject.CommonName, "subject common name")
	require.True(t, slices.Contains(cert.DNSNames, "localhost"), "DNSNames should contain localhost")
	require.NotZero(t, cert.KeyUsage&x509.KeyUsageDigitalSignature, "KeyUsage should have DigitalSignature bit set")
	require.True(t, slices.Contains(cert.ExtKeyUsage, x509.ExtKeyUsageServerAuth), "ExtKeyUsage should contain ServerAuth")

	validity := cert.NotAfter.Sub(cert.NotBefore)
	require.Less(t, validity, 14*24*time.Hour, "validity duration")
	require.Greater(t, validity, 10*24*time.Hour, "validity duration")

	now := time.Now()
	const skew = 2 * time.Minute
	require.False(t, cert.NotBefore.After(now.Add(skew)), "NotBefore should not be in the future")
	require.False(t, cert.NotAfter.Before(now.Add(-skew)), "NotAfter should not be in the past")
}
