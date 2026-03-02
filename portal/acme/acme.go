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
	"os"
	"path/filepath"
	"sync"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	lego "github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"
	"github.com/rs/zerolog/log"
)

const (
	fullChainFileName      = "fullchain.pem"
	keyFileName            = "privatekey.pem"
	accountKeyFileName     = "acme-account.key"
	registrationFileName   = "acme-registration.json"
	defaultACMEEmailPrefix = "acme@"
)

type certTarget struct {
	Name     string
	KeyFile  string
	CertFile string
	Domains  []string
}

type provisionConfig struct {
	TargetName       string
	KeyFile          string
	CertFile         string
	Email            string
	Domains          []string
	AccountKeyFile   string
	RegistrationFile string
	CloudflareToken  string
}

type Config struct {
	BaseDomain      string
	KeyDir          string
	CloudflareToken string
}

type AcmeManager struct {
	cfg       Config
	mu        sync.RWMutex
	stopCh    chan struct{}
	waitGroup sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once
}

func NewManager(cfg Config) *AcmeManager {
	return &AcmeManager{
		cfg: Config{
			BaseDomain:      cfg.BaseDomain,
			KeyDir:          cfg.KeyDir,
			CloudflareToken: cfg.CloudflareToken,
		},
		stopCh: make(chan struct{}),
	}
}

func (m *AcmeManager) keyDir() string {
	if m == nil {
		return ""
	}
	return m.cfg.KeyDir
}

// SigningKeyFile returns the unified signer key path under configured key directory.
func (m *AcmeManager) SigningKeyFile() string {
	if m == nil {
		return ""
	}
	keyDir := m.keyDir()
	if keyDir == "" {
		return ""
	}
	return keyPath(keyDir)
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
	return u.Email
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
func (m *AcmeManager) EnsureSigningKey(ctx context.Context) (string, error) {
	if m == nil {
		return "", errors.New("acme manager is nil")
	}
	configuredKeyDir := m.keyDir()
	if configuredKeyDir == "" {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("keyless provisioning canceled: %w", err)
	}

	baseDomain := m.cfg.BaseDomain
	if baseDomain == "" {
		return "", fmt.Errorf("base domain is required for ACME provisioning")
	}

	targets, err := buildCertTargets(baseDomain, configuredKeyDir)
	if err != nil {
		return "", err
	}
	signerKeyFile := keyPath(configuredKeyDir)

	missingTargets := make([]certTarget, 0, len(targets))
	for _, target := range targets {
		if fileExists(target.KeyFile) && fileExists(target.CertFile) {
			continue
		}
		missingTargets = append(missingTargets, target)
	}
	if len(missingTargets) == 0 {
		return signerKeyFile, nil
	}
	if !hasCloudflareToken(m.cfg.CloudflareToken) {
		if !fileExists(signerKeyFile) {
			log.Warn().
				Str("key_file", signerKeyFile).
				Msg("[signer] keyless key file is missing and Cloudflare credentials are not set; signer will stay disabled")
		}
		for _, target := range missingTargets {
			log.Warn().
				Str("target", target.Name).
				Str("key_file", target.KeyFile).
				Str("cert_file", target.CertFile).
				Msg("[signer] ACME target is missing and Cloudflare credentials are not set")
		}
		return signerKeyFile, nil
	}

	for _, target := range missingTargets {
		cfg, buildErr := buildProvisionConfig(baseDomain, target, m.cfg.CloudflareToken)
		if buildErr != nil {
			return "", buildErr
		}
		log.Info().
			Str("target", cfg.TargetName).
			Strs("domains", cfg.Domains).
			Str("key_file", cfg.KeyFile).
			Str("cert_file", cfg.CertFile).
			Msg("[signer] ACME target is missing; issuing certificate with ACME DNS-01 via Cloudflare")

		if err := m.provisionCertificate(cfg); err != nil {
			return "", err
		}
	}
	return signerKeyFile, nil
}

// TLSFiles returns the unified fullchain and private key file paths when both exist.
func (m *AcmeManager) TLSFiles() (string, string) {
	if m == nil {
		return "", ""
	}
	keyDir := m.keyDir()
	if keyDir == "" {
		return "", ""
	}

	keyFile := keyPath(keyDir)
	certFile := fullChainPath(keyDir)
	if fileExists(certFile) && fileExists(keyFile) {
		return certFile, keyFile
	}

	return "", ""
}

func buildProvisionConfig(baseDomain string, target certTarget, cloudflareToken string) (provisionConfig, error) {
	keyDir := filepath.Dir(target.KeyFile)
	accountKeyFile := filepath.Join(keyDir, accountKeyFileName)
	registrationFile := filepath.Join(keyDir, registrationFileName)
	email := defaultACMEEmailPrefix + baseDomain

	if target.KeyFile == "" || target.CertFile == "" || len(target.Domains) == 0 {
		return provisionConfig{}, errors.New("invalid ACME target")
	}
	if _, err := resolveDomain(baseDomain); err != nil {
		return provisionConfig{}, err
	}

	return provisionConfig{
		TargetName:       target.Name,
		KeyFile:          target.KeyFile,
		CertFile:         target.CertFile,
		Email:            email,
		Domains:          target.Domains,
		AccountKeyFile:   accountKeyFile,
		RegistrationFile: registrationFile,
		CloudflareToken:  cloudflareToken,
	}, nil
}

func resolveDomain(baseDomain string) (string, error) {
	if baseDomain == "" {
		return "", errors.New("base domain is required")
	}
	return baseDomain, nil
}

func buildCertTargets(baseDomain, configuredKeyDir string) ([]certTarget, error) {
	base, err := resolveDomain(baseDomain)
	if err != nil {
		return nil, err
	}
	keyDir := configuredKeyDir
	if keyDir == "" {
		return nil, errors.New("key directory is required")
	}

	return []certTarget{
		{
			Name:     "unified",
			KeyFile:  keyPath(keyDir),
			CertFile: fullChainPath(keyDir),
			Domains:  []string{"*." + base, base},
		},
	}, nil
}

func (m *AcmeManager) provisionCertificate(cfg provisionConfig) error {
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
	cfConfig.AuthToken = cfg.CloudflareToken

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
		Str("target", cfg.TargetName).
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
	if path == "" {
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
	return cloudflareToken != ""
}

func fullChainPath(keyDir string) string {
	return filepath.Join(keyDir, fullChainFileName)
}

func keyPath(keyDir string) string {
	return filepath.Join(keyDir, keyFileName)
}
