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
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	lego "github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
	"github.com/rs/zerolog/log"
)

const (
	fullChainFileName      = "fullchain.pem"
	keyFileName            = "privatekey.pem"
	accountKeyFileName     = "acme-account.key"
	registrationFileName   = "acme-registration.json"
	defaultACMEEmailPrefix = "acme@"
	defaultRenewInterval   = 24 * time.Hour
	defaultDNSSyncInterval = 10 * time.Minute
	defaultSyncTimeout     = 2 * time.Minute
)

type Config struct {
	BaseDomain         string
	KeyDir             string
	DNSProvider        string
	CloudflareToken    string
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSSessionToken    string
	AWSRegion          string
	AWSHostedZoneID    string
}

type Manager struct {
	stopCh    chan struct{}
	cfg       Config
	wg        sync.WaitGroup
	mu        sync.RWMutex
	dns       DNSProvider
	startOnce sync.Once
	stopOnce  sync.Once
}

type provisionConfig struct {
	KeyFile          string
	CertFile         string
	AccountKeyFile   string
	RegistrationFile string
	Email            string
	DNSProvider      DNSProvider
	Domains          []string
}

type acmeUser struct {
	Key          crypto.PrivateKey
	Registration *registration.Resource
	Email        string
}

func NewManager(cfg Config) (*Manager, error) {
	cfg.BaseDomain = normalizeHost(cfg.BaseDomain)
	cfg.KeyDir = strings.TrimSpace(cfg.KeyDir)
	cfg.DNSProvider = strings.ToLower(strings.TrimSpace(cfg.DNSProvider))
	cfg.CloudflareToken = strings.TrimSpace(cfg.CloudflareToken)
	cfg.AWSAccessKeyID = strings.TrimSpace(cfg.AWSAccessKeyID)
	cfg.AWSSecretAccessKey = strings.TrimSpace(cfg.AWSSecretAccessKey)
	cfg.AWSSessionToken = strings.TrimSpace(cfg.AWSSessionToken)
	cfg.AWSRegion = strings.TrimSpace(cfg.AWSRegion)
	cfg.AWSHostedZoneID = strings.TrimSpace(cfg.AWSHostedZoneID)

	if cfg.KeyDir == "" {
		return nil, errors.New("acme key directory is required")
	}
	if cfg.BaseDomain == "" {
		return nil, errors.New("acme base domain is required")
	}
	if isLocalhost(cfg.BaseDomain) {
		return &Manager{
			cfg:    cfg,
			stopCh: make(chan struct{}),
		}, nil
	}

	dns, err := NewDNSProvider(DNSProviderConfig{
		Type:               cfg.DNSProvider,
		CloudflareToken:    cfg.CloudflareToken,
		AWSAccessKeyID:     cfg.AWSAccessKeyID,
		AWSSecretAccessKey: cfg.AWSSecretAccessKey,
		AWSSessionToken:    cfg.AWSSessionToken,
		AWSRegion:          cfg.AWSRegion,
		AWSHostedZoneID:    cfg.AWSHostedZoneID,
	})
	if err != nil {
		return nil, fmt.Errorf("create acme dns provider: %w", err)
	}

	return &Manager{
		cfg:    cfg,
		stopCh: make(chan struct{}),
		dns:    dns,
	}, nil
}

func (m *Manager) EnsureCertificate(ctx context.Context) (string, string, error) {
	if m == nil {
		return "", "", errors.New("acme manager is nil")
	}

	if isLocalhost(m.cfg.BaseDomain) {
		if err := ensureLocalDevelopmentCertificate(m.cfg.KeyDir, m.cfg.BaseDomain); err != nil {
			return "", "", err
		}
		return m.TLSFiles()
	}

	if err := m.syncDNS(ctx); err != nil {
		return "", "", fmt.Errorf("ensure dns records: %w", err)
	}

	certFile, keyFile, err := m.TLSFiles()
	if err == nil {
		covered, err := certCoversDomains(certFile, certificateDomains(m.cfg.BaseDomain))
		if err == nil && covered {
			return certFile, keyFile, nil
		}
	}

	if err := m.provision(ctx); err != nil {
		return "", "", err
	}
	return m.TLSFiles()
}

func (m *Manager) EnsureTLSMaterial(ctx context.Context) ([]byte, []byte, error) {
	certFile, keyFile, err := m.EnsureCertificate(ctx)
	if err != nil {
		return nil, nil, err
	}

	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read api tls certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read api tls private key: %w", err)
	}
	return certPEM, keyPEM, nil
}

func (m *Manager) Start(ctx context.Context) {
	if m == nil || isLocalhost(m.cfg.BaseDomain) {
		return
	}

	m.startOnce.Do(func() {
		m.wg.Add(1)
		go m.maintenanceLoop(ctx)
	})
}

func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	m.wg.Wait()
}

func (m *Manager) TLSFiles() (string, string, error) {
	if m == nil {
		return "", "", errors.New("acme manager is nil")
	}
	certFile := filepath.Join(m.cfg.KeyDir, fullChainFileName)
	keyFile := filepath.Join(m.cfg.KeyDir, keyFileName)
	if !fileExists(certFile) || !fileExists(keyFile) {
		return "", "", errors.New("relay certificate files do not exist")
	}
	return certFile, keyFile, nil
}

func (m *Manager) provision(ctx context.Context) error {
	cfg := provisionConfig{
		KeyFile:          filepath.Join(m.cfg.KeyDir, keyFileName),
		CertFile:         filepath.Join(m.cfg.KeyDir, fullChainFileName),
		AccountKeyFile:   filepath.Join(m.cfg.KeyDir, accountKeyFileName),
		RegistrationFile: filepath.Join(m.cfg.KeyDir, registrationFileName),
		Email:            defaultACMEEmailPrefix + m.cfg.BaseDomain,
		DNSProvider:      m.dns,
		Domains:          certificateDomains(m.cfg.BaseDomain),
	}

	for _, path := range []string{cfg.KeyFile, cfg.CertFile, cfg.AccountKeyFile, cfg.RegistrationFile} {
		if err := ensureParentDir(path); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("acme provisioning canceled: %w", err)
	}

	client, _, err := newClient(ctx, cfg)
	if err != nil {
		return err
	}

	obtained, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: cfg.Domains,
		Bundle:  true,
	})
	if err != nil {
		return fmt.Errorf("obtain certificate: %w", err)
	}
	if len(obtained.Certificate) == 0 || len(obtained.PrivateKey) == 0 {
		return errors.New("acme obtain response missing certificate or private key")
	}

	if err := writeFileAtomic(cfg.CertFile, obtained.Certificate, 0o644); err != nil {
		return fmt.Errorf("write certificate chain: %w", err)
	}
	if err := writeFileAtomic(cfg.KeyFile, obtained.PrivateKey, 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	return nil
}

func (m *Manager) maintenanceLoop(ctx context.Context) {
	defer m.wg.Done()

	renewTicker := time.NewTicker(defaultRenewInterval)
	dnsTicker := time.NewTicker(defaultDNSSyncInterval)
	defer renewTicker.Stop()
	defer dnsTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-dnsTicker.C:
			syncCtx, cancel := context.WithTimeout(ctx, defaultSyncTimeout)
			err := m.syncDNS(syncCtx)
			cancel()
			if err != nil {
				log.Warn().Err(err).Str("base_domain", m.cfg.BaseDomain).Msg("sync dns records")
			}
		case <-renewTicker.C:
			if !m.shouldRenew() {
				continue
			}
			renewCtx, cancel := context.WithTimeout(ctx, defaultSyncTimeout)
			err := m.provision(renewCtx)
			cancel()
			if err != nil {
				log.Warn().Err(err).Str("base_domain", m.cfg.BaseDomain).Msg("renew acme certificate")
			}
		}
	}
}

func (m *Manager) syncDNS(ctx context.Context) error {
	if m == nil || isLocalhost(m.cfg.BaseDomain) {
		return nil
	}
	if m.dns == nil {
		return errors.New("acme dns provider is required")
	}

	publicIP, err := detectPublicIPv4(ctx)
	if err != nil {
		return fmt.Errorf("detect public ip: %w", err)
	}

	return m.dns.EnsureARecords(ctx, m.cfg.BaseDomain, publicIP)
}

func (m *Manager) shouldRenew() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	certFile := filepath.Join(m.cfg.KeyDir, fullChainFileName)
	needsRenewal, err := certNeedsRenewal(certFile, certificateDomains(m.cfg.BaseDomain))
	return err == nil && needsRenewal
}

func certificateDomains(baseDomain string) []string {
	return []string{baseDomain, "*." + baseDomain}
}

func certNeedsRenewal(certFile string, domains []string) (bool, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return false, err
	}
	cert, err := ParseCertificatePEM(certPEM)
	if err != nil {
		return false, err
	}
	if time.Until(cert.NotAfter) < 30*24*time.Hour {
		return true, nil
	}
	covered, err := certCoversDomains(certFile, domains)
	if err != nil {
		return false, err
	}
	return !covered, nil
}

func certCoversDomains(certFile string, domains []string) (bool, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return false, err
	}
	cert, err := ParseCertificatePEM(certPEM)
	if err != nil {
		return false, err
	}
	for _, domain := range domains {
		if wildcardDomain, ok := strings.CutPrefix(domain, "*."); ok {
			if !certificateCoversHostname(cert, "probe."+wildcardDomain) {
				return false, nil
			}
			continue
		}
		if !certificateCoversHostname(cert, domain) {
			return false, nil
		}
	}
	return true, nil
}

func certificateCoversHostname(cert *x509.Certificate, hostname string) bool {
	return cert != nil && cert.VerifyHostname(hostname) == nil
}

func newClient(ctx context.Context, cfg provisionConfig) (*lego.Client, *acmeUser, error) {
	accountKey, err := loadOrCreateAccountKey(cfg.AccountKeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("load acme account key: %w", err)
	}
	accountReg, err := loadRegistration(cfg.RegistrationFile)
	if err != nil {
		return nil, nil, fmt.Errorf("load acme registration: %w", err)
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
		return nil, nil, fmt.Errorf("create acme client: %w", err)
	}

	if cfg.DNSProvider == nil {
		return nil, nil, errors.New("acme dns provider is required")
	}
	challengeProvider, err := cfg.DNSProvider.ChallengeProvider(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("create dns challenge provider: %w", err)
	}
	if err := client.Challenge.SetDNS01Provider(challengeProvider); err != nil {
		return nil, nil, fmt.Errorf("set dns01 provider: %w", err)
	}

	if user.Registration == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, nil, fmt.Errorf("register acme account: %w", err)
		}
		user.Registration = reg
		if err := saveRegistration(cfg.RegistrationFile, reg); err != nil {
			return nil, nil, fmt.Errorf("persist acme registration: %w", err)
		}
	}

	return client, user, nil
}

func (u *acmeUser) GetEmail() string                        { return u.Email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.Registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.Key }

func loadOrCreateAccountKey(path string) (crypto.PrivateKey, error) {
	keyPEM, err := os.ReadFile(path)
	if err == nil {
		return parsePEMPrivateKey(keyPEM)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate account key: %w", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal account key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	if err := writeFileAtomic(path, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("persist account key: %w", err)
	}
	return key, nil
}

func parsePEMPrivateKey(keyPEM []byte) (crypto.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("invalid private key pem")
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
	return os.MkdirAll(dir, 0o700)
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
	defer func() { _ = os.Remove(tmpName) }()

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
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func ParseCertificatePEM(pemData []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("no pem block found")
	}
	return x509.ParseCertificate(block.Bytes)
}

func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(host, "*.")
	host = strings.TrimSuffix(host, ".")
	return host
}

func isLocalhost(host string) bool {
	host = normalizeHost(host)
	switch host {
	case "", "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.HasSuffix(host, ".localhost")
}

func detectPublicIPv4(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api4.ipify.org", nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(body))
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return "", fmt.Errorf("invalid ipv4 address: %q", ip)
	}
	return ip, nil
}
