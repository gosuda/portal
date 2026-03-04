package keyless

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	keylesstls "github.com/gosuda/keyless_tls/keyless"

	"gosuda.org/portal/types"
)

// BuildClientTLSConfig builds a keyless TLS server config for tunnel-side TLS termination.
// It returns the TLS config and a close callback for signer resources.
func BuildClientTLSConfig(relayAddr, keylessServerName, domain string) (*tls.Config, func(), error) {
	if keylessServerName == "" {
		return nil, nil, errors.New("keyless server name is required")
	}
	if domain == "" {
		return nil, nil, errors.New("tls domain is required")
	}
	certPEM, rootCAPEM, err := ResolveMaterials(
		context.Background(),
		relayAddr,
		keylessServerName,
		nil,
		nil,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("prepare keyless materials: %w", err)
	}

	if verifyErr := VerifyCertificateHostname(certPEM, domain); verifyErr != nil {
		return nil, nil, fmt.Errorf("keyless certificate does not cover %s: %w", domain, verifyErr)
	}

	remoteSigner, err := keylesstls.NewRemoteSigner(keylesstls.RemoteSignerConfig{
		Endpoint:   relayAddr,
		ServerName: keylessServerName,
		KeyID:      RelayKeyID,
		EnableMTLS: false,
		RootCAPEM:  rootCAPEM,
	}, certPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("create keyless remote signer: %w", err)
	}

	tlsConfig, err := keylesstls.NewServerTLSConfig(keylesstls.ServerTLSConfig{
		CertPEM: certPEM,
		Signer:  remoteSigner,
	})
	if err != nil {
		_ = remoteSigner.Close()
		return nil, nil, fmt.Errorf("create keyless TLS config: %w", err)
	}
	tlsConfig.NextProtos = []string{"http/1.1"}

	return tlsConfig, func() { _ = remoteSigner.Close() }, nil
}

// ResolveMaterials prepares certificate chain and root CAs for keyless TLS mode.
func ResolveMaterials(
	ctx context.Context,
	keylessEndpoint string,
	keylessServerName string,
	inlineCertPEM []byte,
	inlineRootCAPEM []byte,
) ([]byte, []byte, error) {
	certPEM := append([]byte(nil), inlineCertPEM...)
	rootCAPEM := append([]byte(nil), inlineRootCAPEM...)

	// If both are explicitly provided, no need for endpoint fetch.
	if len(certPEM) > 0 && len(rootCAPEM) > 0 {
		return certPEM, rootCAPEM, nil
	}

	chainFromEndpoint, err := FetchEndpointCertificateChain(ctx, keylessEndpoint, keylessServerName)
	if err != nil && len(certPEM) == 0 {
		return nil, nil, fmt.Errorf("auto-discover certificate chain from signer endpoint: %w", err)
	}
	if err != nil {
		log.Debug().Err(err).Msg("[SDK] Failed to fetch cert from endpoint, using inline materials")
	}

	if len(certPEM) == 0 {
		certPEM = chainFromEndpoint
	}
	if len(certPEM) == 0 {
		return nil, nil, errors.New("keyless certificate chain is required")
	}

	if len(rootCAPEM) == 0 && len(chainFromEndpoint) > 0 {
		rootCAPEM = append([]byte(nil), chainFromEndpoint...)
	}
	if len(rootCAPEM) == 0 {
		rootCAPEM = append([]byte(nil), certPEM...)
	}

	return certPEM, rootCAPEM, nil
}

// VerifyCertificateHostname checks whether the leaf cert covers hostname.
func VerifyCertificateHostname(certPEM []byte, hostname string) error {
	_, leaf, err := ParseCertificateChainPEM(certPEM)
	if err != nil {
		return err
	}
	return leaf.VerifyHostname(hostname)
}

// ParseCertificateChainPEM parses PEM cert chain and returns DER chain + leaf.
func ParseCertificateChainPEM(certPEM []byte) ([][]byte, *x509.Certificate, error) {
	if len(certPEM) == 0 {
		return nil, nil, errors.New("certificate PEM is empty")
	}

	var chain [][]byte
	rest := certPEM
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			chain = append(chain, block.Bytes)
		}
		rest = next
	}
	if len(chain) == 0 {
		return nil, nil, errors.New("no certificate blocks found")
	}

	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return nil, nil, fmt.Errorf("parse leaf certificate: %w", err)
	}

	return chain, leaf, nil
}

// FetchEndpointCertificateChain fetches peer cert chain from signer endpoint.
func FetchEndpointCertificateChain(ctx context.Context, endpoint string, serverName string) ([]byte, error) {
	raw := endpoint
	if raw == "" {
		return nil, errors.New("endpoint is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint URL: %w", err)
	}
	if u.Scheme == "http" {
		return nil, errors.New("http signer endpoint does not expose TLS certificate chain (use https endpoint)")
	}

	host := u.Hostname()
	if host == "" {
		return nil, errors.New("endpoint hostname is empty")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	if serverName == "" {
		serverName = host
	}

	dialer := &net.Dialer{Timeout: 5 * time.Second}
	rawConn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("dial signer endpoint: %w", err)
	}

	tlsConn := tls.Client(rawConn, &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         serverName,
		InsecureSkipVerify: types.IsLocalhost(host),
	})
	defer tlsConn.Close()
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("TLS handshake with signer endpoint: %w", err)
	}

	peerCerts := tlsConn.ConnectionState().PeerCertificates
	if len(peerCerts) == 0 {
		return nil, errors.New("no peer certificates from signer endpoint")
	}

	var chainPEM []byte
	for _, cert := range peerCerts {
		chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: cert.Raw,
		})...)
	}
	return chainPEM, nil
}
