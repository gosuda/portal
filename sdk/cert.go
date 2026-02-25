package sdk

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"time"
)

// CertificateClient handles certificate generation and CSR submission
type CertificateClient struct {
	relayAPIURL string
	httpClient  *http.Client
}

// NewCertificateClient creates a new certificate client
func NewCertificateClient(relayAPIURL string) *CertificateClient {
	return &CertificateClient{
		relayAPIURL: relayAPIURL,
		httpClient:  &http.Client{Timeout: 60 * time.Second},
	}
}

// KeyPair represents a generated key pair with the private key
type KeyPair struct {
	PrivateKey *ecdsa.PrivateKey
	PublicKey  *ecdsa.PublicKey
}

// GenerateKeyPair generates a new ECDSA P-256 key pair
func GenerateKeyPair() (*KeyPair, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key pair: %w", err)
	}
	return &KeyPair{
		PrivateKey: privateKey,
		PublicKey:  &privateKey.PublicKey,
	}, nil
}

// CreateCSR creates a PEM-encoded Certificate Signing Request
func CreateCSR(keyPair *KeyPair, domain string) ([]byte, error) {
	template := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName: domain,
		},
		DNSNames: []string{domain},
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, keyPair.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("create CSR: %w", err)
	}

	csrPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	})

	return csrPEM, nil
}

// RequestCertificate requests a certificate from the relay.
// The relay derives the domain from leaseName + base domain.
// The CSR must contain the full domain (constructed from GetBaseDomain + leaseName).
func (c *CertificateClient) RequestCertificate(ctx context.Context, leaseID, reverseToken string, csrPEM []byte) (*CSRResponse, error) {
	reqBody := CSRRequest{
		LeaseID:      leaseID,
		ReverseToken: reverseToken,
		CSR:          csrPEM,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.relayAPIURL + "/api/csr"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var csrResp CSRResponse
	if err := json.Unmarshal(respBody, &csrResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if !csrResp.Success {
		return nil, fmt.Errorf("certificate request failed: %s", csrResp.Message)
	}

	return &csrResp, nil
}

// GetBaseDomain fetches the relay's base domain for TLS certificate construction.
func (c *CertificateClient) GetBaseDomain(ctx context.Context) (string, error) {
	url := c.relayAPIURL + "/api/domain"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var domainResp struct {
		BaseDomain string `json:"base_domain"`
	}
	if err := json.Unmarshal(respBody, &domainResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if domainResp.BaseDomain == "" {
		return "", fmt.Errorf("relay did not return base domain")
	}

	return domainResp.BaseDomain, nil
}

// PrivateKeyToPEM converts a private key to PEM format
func PrivateKeyToPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: der,
	}), nil
}
