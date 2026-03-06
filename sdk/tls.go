package sdk

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"time"

	keylesslib "github.com/gosuda/keyless_tls/keyless"

	"gosuda.org/portal/portal"
)

func buildTenantTLSConfig(cfg portal.TLSMaterialConfig) (*tls.Config, io.Closer, error) {
	if len(cfg.CertPEM) == 0 {
		return nil, nil, fmt.Errorf("tenant certificate is required")
	}
	if cfg.Keyless != nil {
		remoteSigner, err := keylesslib.NewRemoteSigner(keylesslib.RemoteSignerConfig{
			Endpoint:      cfg.Keyless.Endpoint,
			ServerName:    cfg.Keyless.ServerName,
			KeyID:         cfg.Keyless.KeyID,
			ClientCertPEM: cfg.Keyless.ClientCertPEM,
			ClientKeyPEM:  cfg.Keyless.ClientKeyPEM,
			RootCAPEM:     cfg.Keyless.RootCAPEM,
		}, cfg.CertPEM)
		if err != nil {
			return nil, nil, err
		}
		tlsConf, err := keylesslib.NewServerTLSConfig(keylesslib.ServerTLSConfig{
			CertPEM:    cfg.CertPEM,
			Signer:     remoteSigner,
			NextProtos: []string{"http/1.1"},
			MinVersion: tls.VersionTLS12,
		})
		if err != nil {
			_ = remoteSigner.Close()
			return nil, nil, err
		}
		return tlsConf, remoteSigner, nil
	}

	cert, err := tls.X509KeyPair(cfg.CertPEM, cfg.KeyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("parse tenant tls key pair: %w", err)
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
		Certificates: []tls.Certificate{cert},
	}, nil, nil
}

func buildAutoTenantTLSConfig(hostnames []string) (*tls.Config, error) {
	certPEM, keyPEM, err := selfSignedTenantCert(hostnames)
	if err != nil {
		return nil, err
	}
	tlsConf, _, err := buildTenantTLSConfig(portal.TLSMaterialConfig{
		CertPEM: certPEM,
		KeyPEM:  keyPEM,
	})
	return tlsConf, err
}

func selfSignedTenantCert(hostnames []string) (certPEM, keyPEM []byte, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate self-signed tenant key: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, fmt.Errorf("generate self-signed tenant serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: firstHostname(hostnames),
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(30 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	for _, host := range hostnames {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
			continue
		}
		template.DNSNames = append(template.DNSNames, host)
	}
	if len(template.DNSNames) == 0 && len(template.IPAddresses) == 0 {
		template.DNSNames = []string{"localhost"}
		template.Subject.CommonName = "localhost"
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("create self-signed tenant certificate: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal self-signed tenant key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

func firstHostname(hostnames []string) string {
	for _, host := range hostnames {
		host = strings.TrimSpace(host)
		if host != "" {
			return host
		}
	}
	return "localhost"
}
