package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"gosuda.org/portal/types"
)

const localDevelopmentCertificateTTL = 3650 * 24 * time.Hour

// EnsureLocalDevelopmentCertificate ensures keyless TLS materials exist for localhost-style development.
// It only acts when baseHost points to localhost/loopback semantics.
func EnsureLocalDevelopmentCertificate(keyDir, baseHost string) (bool, error) {
	keyDir = strings.TrimSpace(keyDir)
	if keyDir == "" {
		return false, nil
	}

	baseHost = normalizeLocalDevelopmentHost(baseHost)
	if !types.IsLocalhost(baseHost) {
		return false, nil
	}

	domains := localDevelopmentDomains(baseHost)
	keyFile := keyPath(keyDir)
	certFile := fullChainPath(keyDir)

	if fileExists(keyFile) && fileExists(certFile) {
		covered, err := certCoversDomains(certFile, domains)
		if err == nil && covered {
			return false, nil
		}
	}

	if err := ensureParentDir(keyFile); err != nil {
		return false, err
	}
	if err := ensureParentDir(certFile); err != nil {
		return false, err
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return false, fmt.Errorf("generate local development signing key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return false, fmt.Errorf("generate local development certificate serial: %w", err)
	}

	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   baseHost,
			Organization: []string{"Portal Local Development"},
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.Add(localDevelopmentCertificateTTL),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageKeyAgreement | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	dnsNames := make(map[string]struct{}, len(domains))
	ipAddresses := make(map[string]net.IP)
	for _, domain := range domains {
		if ip := net.ParseIP(domain); ip != nil {
			ipAddresses[ip.String()] = ip
			continue
		}
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		dnsNames[domain] = struct{}{}
	}

	for dnsName := range dnsNames {
		template.DNSNames = append(template.DNSNames, dnsName)
	}
	for _, ipAddress := range ipAddresses {
		template.IPAddresses = append(template.IPAddresses, ipAddress)
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return false, fmt.Errorf("create local development certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return false, fmt.Errorf("marshal local development private key: %w", err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})

	if err := writeFileAtomic(keyFile, privateKeyPEM, 0o600); err != nil {
		return false, fmt.Errorf("write local development private key: %w", err)
	}
	if err := writeFileAtomic(certFile, certPEM, 0o644); err != nil {
		return false, fmt.Errorf("write local development certificate: %w", err)
	}

	return true, nil
}

func normalizeLocalDevelopmentHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(strings.TrimSuffix(host, "."), "*.")
	return host
}

func localDevelopmentDomains(baseHost string) []string {
	baseHost = normalizeLocalDevelopmentHost(baseHost)
	if baseHost == "" {
		return []string{"localhost", "*.localhost", "127.0.0.1", "::1"}
	}

	domains := []string{"localhost", "*.localhost", "127.0.0.1", "::1"}
	domains = append(domains, baseHost)

	if net.ParseIP(baseHost) == nil {
		domains = append(domains, "*."+baseHost)
	}

	seen := make(map[string]struct{}, len(domains))
	out := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		out = append(out, domain)
	}
	return out
}
