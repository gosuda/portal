package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	lego "github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"
	"github.com/rs/zerolog/log"
)

const (
	fullChainFileName      = "fullchain.pem"
	accountKeyFileName     = "acme-account.key"
	registrationFileName   = "acme-registration.json"
	defaultACMEEmailPrefix = "acme@"
)

type provisionConfig struct {
	KeyFile          string
	CertFile         string
	Email            string
	Domains          []string
	AccountKeyFile   string
	RegistrationFile string
	CloudflareToken  string
}

type Config struct {
	PortalURL       string
	KeyFile         string
	CloudflareToken string
}

type Manager struct {
	cfg Config
}

func NewManager(cfg Config) *Manager {
	return &Manager{
		cfg: Config{
			PortalURL:       strings.TrimSpace(cfg.PortalURL),
			KeyFile:         strings.TrimSpace(cfg.KeyFile),
			CloudflareToken: strings.TrimSpace(cfg.CloudflareToken),
		},
	}
}

func (m *Manager) keyFile() string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m.cfg.KeyFile)
}

type acmeUser struct {
	Email        string
	Registration *registration.Resource
	Key          crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string {
	if u == nil {
		return ""
	}
	return strings.TrimSpace(u.Email)
}

func (u *acmeUser) GetRegistration() *registration.Resource {
	if u == nil {
		return nil
	}
	return u.Registration
}

func (u *acmeUser) GetPrivateKey() crypto.PrivateKey {
	if u == nil {
		return nil
	}
	return u.Key
}

// EnsureSigningKey provisions a keyless signing key via ACME DNS-01 when missing.
func (m *Manager) EnsureSigningKey(ctx context.Context) (string, error) {
	if m == nil {
		return "", errors.New("acme manager is nil")
	}
	keyFile := m.keyFile()
	if keyFile == "" {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("keyless provisioning canceled: %w", err)
	}

	if fileExists(keyFile) {
		return keyFile, nil
	}
	if !hasCloudflareToken(m.cfg.CloudflareToken) {
		log.Warn().
			Str("key_file", keyFile).
			Msg("[signer] keyless key file is missing and Cloudflare credentials are not set; signer will stay disabled")
		return keyFile, nil
	}

	baseDomain := extractBaseDomain(m.cfg.PortalURL)
	if baseDomain == "" {
		return "", fmt.Errorf("derive base domain from PORTAL_URL for ACME provisioning")
	}

	cfg, err := buildProvisionConfig(baseDomain, keyFile, m.cfg.CloudflareToken)
	if err != nil {
		return "", err
	}

	log.Info().
		Strs("domains", cfg.Domains).
		Str("key_file", cfg.KeyFile).
		Str("cert_file", cfg.CertFile).
		Msg("[signer] keyless key is missing; issuing certificate with ACME DNS-01 via Cloudflare")

	if err := m.provisionCertificate(cfg); err != nil {
		return "", err
	}
	return keyFile, nil
}

// TLSFiles returns fullchain and private key file paths when both exist.
func (m *Manager) TLSFiles() (string, string) {
	if m == nil {
		return "", ""
	}
	keyFile := m.keyFile()
	if keyFile == "" {
		return "", ""
	}
	certFile := fullChainPath(keyFile)
	if _, err := os.Stat(certFile); err != nil {
		return "", ""
	}
	if _, err := os.Stat(keyFile); err != nil {
		return "", ""
	}
	return certFile, keyFile
}

func buildProvisionConfig(baseDomain, keyFile, cloudflareToken string) (provisionConfig, error) {
	keyDir := filepath.Dir(keyFile)
	certFile := fullChainPath(keyFile)
	accountKeyFile := filepath.Join(keyDir, accountKeyFileName)
	registrationFile := filepath.Join(keyDir, registrationFileName)
	email := defaultACMEEmailPrefix + baseDomain

	domains, err := resolveDomains(baseDomain)
	if err != nil {
		return provisionConfig{}, err
	}

	return provisionConfig{
		KeyFile:          keyFile,
		CertFile:         certFile,
		Email:            email,
		Domains:          domains,
		AccountKeyFile:   accountKeyFile,
		RegistrationFile: registrationFile,
		CloudflareToken:  strings.TrimSpace(cloudflareToken),
	}, nil
}

func resolveDomains(baseDomain string) ([]string, error) {
	domain := strings.ToLower(strings.TrimSpace(baseDomain))
	if domain == "" {
		return nil, errors.New("base domain is required")
	}
	return []string{domain, "*." + domain}, nil
}

func (m *Manager) provisionCertificate(cfg provisionConfig) error {
	for _, path := range []string{cfg.KeyFile, cfg.CertFile, cfg.AccountKeyFile, cfg.RegistrationFile} {
		if err := ensureParentDir(path); err != nil {
			return err
		}
	}

	accountKey, err := loadOrCreateAccountKey(cfg.AccountKeyFile)
	if err != nil {
		return fmt.Errorf("load ACME account key: %w", err)
	}
	accountReg, err := loadRegistration(cfg.RegistrationFile)
	if err != nil {
		return fmt.Errorf("load ACME registration: %w", err)
	}

	user := &acmeUser{
		Email:        cfg.Email,
		Key:          accountKey,
		Registration: accountReg,
	}
	clientConfig := lego.NewConfig(user)
	clientConfig.CADirURL = lego.LEDirectoryProduction
	clientConfig.Certificate.KeyType = certcrypto.RSA2048

	client, err := lego.NewClient(clientConfig)
	if err != nil {
		return fmt.Errorf("create ACME client: %w", err)
	}

	cfConfig := cloudflare.NewDefaultConfig()
	cfConfig.AuthToken = strings.TrimSpace(cfg.CloudflareToken)

	provider, err := cloudflare.NewDNSProviderConfig(cfConfig)
	if err != nil {
		return fmt.Errorf("create Cloudflare DNS provider: %w", err)
	}
	if err := client.Challenge.SetDNS01Provider(provider); err != nil {
		return fmt.Errorf("set DNS-01 challenge provider: %w", err)
	}

	if user.Registration == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{
			TermsOfServiceAgreed: true,
		})
		if err != nil {
			return fmt.Errorf("register ACME account: %w", err)
		}
		user.Registration = reg
		if err := saveRegistration(cfg.RegistrationFile, reg); err != nil {
			return fmt.Errorf("persist ACME registration: %w", err)
		}
	}

	obtained, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: cfg.Domains,
		Bundle:  true,
	})
	if err != nil {
		return fmt.Errorf("obtain certificate: %w", err)
	}
	if len(obtained.PrivateKey) == 0 {
		return errors.New("ACME response did not include private key")
	}
	if len(obtained.Certificate) == 0 {
		return errors.New("ACME response did not include certificate chain")
	}

	if err := writeFileAtomic(cfg.KeyFile, obtained.PrivateKey, 0o600); err != nil {
		return fmt.Errorf("write keyless private key: %w", err)
	}
	if err := writeFileAtomic(cfg.CertFile, obtained.Certificate, 0o644); err != nil {
		return fmt.Errorf("write keyless certificate chain: %w", err)
	}

	log.Info().
		Str("key_file", cfg.KeyFile).
		Str("cert_file", cfg.CertFile).
		Strs("domains", cfg.Domains).
		Msg("[signer] ACME certificate issued and stored")
	return nil
}

func loadOrCreateAccountKey(path string) (crypto.PrivateKey, error) {
	keyPEM, err := os.ReadFile(path)
	if err == nil {
		key, parseErr := parsePEMPrivateKey(keyPEM)
		if parseErr != nil {
			return nil, parseErr
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ACME account key: %w", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal ACME account key: %w", err)
	}
	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8,
	})
	if err := writeFileAtomic(path, pemData, 0o600); err != nil {
		return nil, fmt.Errorf("persist ACME account key: %w", err)
	}
	return key, nil
}

func parsePEMPrivateKey(keyPEM []byte) (crypto.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("invalid private key PEM")
	}

	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		switch typed := key.(type) {
		case *ecdsa.PrivateKey:
			return typed, nil
		case *rsa.PrivateKey:
			return typed, nil
		}
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("unsupported private key type")
}

func loadRegistration(path string) (*registration.Resource, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var reg registration.Resource
	if err := json.Unmarshal(raw, &reg); err != nil {
		return nil, err
	}
	return &reg, nil
}

func saveRegistration(path string, reg *registration.Resource) error {
	if reg == nil {
		return nil
	}
	raw, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw, 0o600)
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	return nil
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Remove(path)
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func hasCloudflareToken(cloudflareToken string) bool {
	return strings.TrimSpace(cloudflareToken) != ""
}

func extractBaseDomain(portalURL string) string {
	raw := strings.TrimSpace(portalURL)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if parsed.Hostname() == "" {
		return ""
	}

	host := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(parsed.Hostname())), "*.")
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}

func fullChainPath(keyFile string) string {
	return filepath.Join(filepath.Dir(keyFile), fullChainFileName)
}
