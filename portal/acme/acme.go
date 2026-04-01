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
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	lego "github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/utils"
)

const (
	fullChainFileName         = "fullchain.pem"
	keyFileName               = "privatekey.pem"
	accountKeyFileName        = "acme-account.key"
	registrationFileName      = "acme-registration.json"
	gaslessENSTXTPrefix       = "ENS1 "
	defaultENSGaslessResolver = "0x238A8F792dFA6033814B18618aD4100654aeef01"
	defaultACMEEmailPrefix    = "acme@"
	defaultRenewInterval      = 24 * time.Hour
	defaultDNSSyncInterval    = 10 * time.Minute
	defaultSyncTimeout        = 2 * time.Minute
)

type Config struct {
	BaseDomain         string
	KeyDir             string
	DNSProvider        string
	ENSGaslessEnabled  bool
	ENSGaslessAddress  string
	CloudflareToken    string
	AWSAccessKeyID     string
	AWSSecretAccessKey string
	AWSSessionToken    string
	AWSRegion          string
	AWSHostedZoneID    string
	AWSKMSKeyARN       string
	DNSSECKSKName      string
}

type Manager struct {
	stopCh        chan struct{}
	cfg           Config
	wg            sync.WaitGroup
	dns           DNSProvider
	startOnce     sync.Once
	stopOnce      sync.Once
	dnssecLogOnce sync.Once
	ensLogOnce    sync.Once
}

type acmeUser struct {
	Key          crypto.PrivateKey
	Registration *registration.Resource
	Email        string
}

func NewManager(cfg Config) (*Manager, error) {
	cfg.BaseDomain = strings.TrimPrefix(utils.NormalizeHostname(cfg.BaseDomain), "*.")
	cfg.KeyDir = strings.TrimSpace(cfg.KeyDir)
	cfg.DNSProvider = strings.ToLower(strings.TrimSpace(cfg.DNSProvider))
	cfg.ENSGaslessAddress = strings.TrimSpace(cfg.ENSGaslessAddress)
	cfg.CloudflareToken = strings.TrimSpace(cfg.CloudflareToken)
	cfg.AWSAccessKeyID = strings.TrimSpace(cfg.AWSAccessKeyID)
	cfg.AWSSecretAccessKey = strings.TrimSpace(cfg.AWSSecretAccessKey)
	cfg.AWSSessionToken = strings.TrimSpace(cfg.AWSSessionToken)
	cfg.AWSRegion = strings.TrimSpace(cfg.AWSRegion)
	cfg.AWSHostedZoneID = strings.TrimSpace(cfg.AWSHostedZoneID)
	cfg.AWSKMSKeyARN = strings.TrimSpace(cfg.AWSKMSKeyARN)
	cfg.DNSSECKSKName = strings.TrimSpace(cfg.DNSSECKSKName)
	if cfg.ENSGaslessEnabled {
		if cfg.ENSGaslessAddress == "" {
			return nil, errors.New("ens gasless address is required when ens gasless import is enabled")
		}
		address, err := utils.NormalizeEVMAddress(cfg.ENSGaslessAddress)
		if err != nil {
			return nil, fmt.Errorf("normalize ens gasless address: %w", err)
		}
		cfg.ENSGaslessAddress = address
	}

	if cfg.KeyDir == "" {
		return nil, errors.New("acme key directory is required")
	}
	if cfg.BaseDomain == "" {
		return nil, errors.New("acme base domain is required")
	}
	if utils.IsLocalRelayHost(cfg.BaseDomain) {
		return &Manager{
			cfg:    cfg,
			stopCh: make(chan struct{}),
		}, nil
	}

	manager := &Manager{
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}

	acmeDNS, err := NewDNSProvider(cfg.DNSProvider, cfg)
	if err != nil {
		return nil, fmt.Errorf("create acme dns provider: %w", err)
	}
	manager.dns = acmeDNS

	if cfg.ENSGaslessEnabled && manager.dns == nil {
		return nil, errors.New("ens gasless automation requires ACME_DNS_PROVIDER")
	}

	return manager, nil
}

func (m *Manager) EnsureCertificate(ctx context.Context) (string, string, error) {
	if m == nil {
		return "", "", errors.New("acme manager is nil")
	}

	if utils.IsLocalRelayHost(m.cfg.BaseDomain) {
		if err := ensureLocalDevelopmentCertificate(m.cfg.KeyDir, m.cfg.BaseDomain); err != nil {
			return "", "", err
		}
		return m.TLSFiles()
	}
	certFile, keyFile, manual, err := m.manualCertificateOverride()
	if err != nil {
		return "", "", err
	}
	if manual {
		if err := m.syncENSGasless(ctx); err != nil {
			return "", "", err
		}
		return certFile, keyFile, nil
	}
	if !m.managedACME() {
		return m.ensureManualCertificate()
	}

	if err := m.syncDNS(ctx); err != nil {
		return "", "", fmt.Errorf("ensure dns records: %w", err)
	}

	certFile, keyFile, err = m.TLSFiles()
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
	if m == nil || utils.IsLocalRelayHost(m.cfg.BaseDomain) || (!m.cfg.ENSGaslessEnabled && !m.managedACME()) {
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

func (m *Manager) managedACME() bool {
	return m != nil && m.dns != nil
}

func (m *Manager) ensureManualCertificate() (string, string, error) {
	certFile, keyFile, err := m.TLSFiles()
	if err != nil {
		return "", "", fmt.Errorf("manual certificate mode requires %s and %s in %s or configure ACME_DNS_PROVIDER", fullChainFileName, keyFileName, m.cfg.KeyDir)
	}

	covered, err := m.manualCertificateCovered(certFile)
	if err != nil {
		return "", "", err
	}
	if !covered {
		return "", "", fmt.Errorf("manual relay certificate must cover %s and *.%s", m.cfg.BaseDomain, m.cfg.BaseDomain)
	}
	return certFile, keyFile, nil
}

func (m *Manager) manualCertificateOverride() (string, string, bool, error) {
	if m == nil || utils.IsLocalRelayHost(m.cfg.BaseDomain) {
		return "", "", false, nil
	}
	certFile := filepath.Join(m.cfg.KeyDir, fullChainFileName)
	keyFile := filepath.Join(m.cfg.KeyDir, keyFileName)
	if !fileExists(certFile) || !fileExists(keyFile) {
		return "", "", false, nil
	}
	var err error
	covered, err := m.manualCertificateCovered(certFile)
	if err != nil {
		return "", "", false, err
	}
	hasACMEState := m.hasACMEState()
	if !covered {
		if !hasACMEState {
			return "", "", false, fmt.Errorf("manual relay certificate must cover %s and *.%s", m.cfg.BaseDomain, m.cfg.BaseDomain)
		}
		return "", "", false, nil
	}
	if hasACMEState {
		return "", "", false, nil
	}
	return certFile, keyFile, true, nil
}

func (m *Manager) manualCertificateCovered(certFile string) (bool, error) {
	covered, err := certCoversDomains(certFile, certificateDomains(m.cfg.BaseDomain))
	if err != nil {
		return false, fmt.Errorf("validate relay certificate: %w", err)
	}
	return covered, nil
}

func (m *Manager) hasACMEState() bool {
	if m == nil {
		return false
	}
	return fileExists(filepath.Join(m.cfg.KeyDir, accountKeyFileName)) || fileExists(filepath.Join(m.cfg.KeyDir, registrationFileName))
}

func (m *Manager) provision(ctx context.Context) error {
	keyFile := filepath.Join(m.cfg.KeyDir, keyFileName)
	certFile := filepath.Join(m.cfg.KeyDir, fullChainFileName)
	accountKeyFile := filepath.Join(m.cfg.KeyDir, accountKeyFileName)
	registrationFile := filepath.Join(m.cfg.KeyDir, registrationFileName)
	domains := certificateDomains(m.cfg.BaseDomain)

	for _, path := range []string{keyFile, certFile, accountKeyFile, registrationFile} {
		if err := ensureParentDir(path); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("acme provisioning canceled: %w", err)
	}

	client, err := newClient(ctx, defaultACMEEmailPrefix+m.cfg.BaseDomain, accountKeyFile, registrationFile, m.dns)
	if err != nil {
		return err
	}

	obtained, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: domains,
		Bundle:  true,
	})
	if err != nil {
		return fmt.Errorf("obtain certificate: %w", err)
	}
	if len(obtained.Certificate) == 0 || len(obtained.PrivateKey) == 0 {
		return errors.New("acme obtain response missing certificate or private key")
	}

	if err := writeFileAtomic(certFile, obtained.Certificate, 0o644); err != nil {
		return fmt.Errorf("write certificate chain: %w", err)
	}
	if err := writeFileAtomic(keyFile, obtained.PrivateKey, 0o600); err != nil {
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
			_, _, manual, err := m.manualCertificateOverride()
			if err != nil || manual || !m.managedACME() || !m.shouldRenew() {
				continue
			}
			renewCtx, cancel := context.WithTimeout(ctx, defaultSyncTimeout)
			err = m.provision(renewCtx)
			cancel()
			if err != nil {
				log.Warn().Err(err).Str("base_domain", m.cfg.BaseDomain).Msg("renew acme certificate")
			}
		}
	}
}

func (m *Manager) syncDNS(ctx context.Context) error {
	if m == nil || utils.IsLocalRelayHost(m.cfg.BaseDomain) {
		return nil
	}
	if err := m.syncENSGasless(ctx); err != nil {
		return err
	}
	_, _, manual, err := m.manualCertificateOverride()
	if err != nil {
		return err
	}
	if manual || !m.managedACME() {
		return nil
	}

	publicIP, err := utils.ResolvePublicIPv4(ctx)
	if err != nil {
		return fmt.Errorf("detect public ip: %w", err)
	}

	return m.dns.EnsureARecords(ctx, m.cfg.BaseDomain, publicIP)
}

func (m *Manager) syncENSGasless(ctx context.Context) error {
	if m == nil || !m.cfg.ENSGaslessEnabled || utils.IsLocalRelayHost(m.cfg.BaseDomain) {
		return nil
	}
	if m.dns == nil {
		return errors.New("ACME_DNS_PROVIDER is required")
	}

	status, err := m.dns.EnsureDNSSEC(ctx, m.cfg.BaseDomain)
	if err != nil {
		return fmt.Errorf("ensure dnssec: %w", err)
	}
	m.dnssecLogOnce.Do(func() {
		event := log.Info().
			Str("provider", m.dns.Name()).
			Str("base_domain", m.cfg.BaseDomain).
			Str("state", strings.TrimSpace(status.State))
		if strings.TrimSpace(status.DSRecord) != "" {
			event = event.Str("ds_record", strings.TrimSpace(status.DSRecord))
		}
		if strings.TrimSpace(status.Message) != "" {
			event = event.Str("message", strings.TrimSpace(status.Message))
		}
		event.Msg("dnssec configured")
	})

	if err := m.SyncENSGaslessHostname(ctx, m.cfg.BaseDomain, m.cfg.ENSGaslessAddress); err != nil {
		return fmt.Errorf("ensure ens gasless txt: %w", err)
	}
	m.ensLogOnce.Do(func() {
		log.Info().
			Str("provider", m.dns.Name()).
			Str("base_domain", m.cfg.BaseDomain).
			Str("address", m.cfg.ENSGaslessAddress).
			Msg("ens gasless dns import configured")
	})
	return nil
}

func (m *Manager) SyncENSGaslessHostname(ctx context.Context, hostname, address string) error {
	if m == nil || !m.cfg.ENSGaslessEnabled || utils.IsLocalRelayHost(m.cfg.BaseDomain) {
		return nil
	}
	if m.dns == nil {
		return errors.New("ACME_DNS_PROVIDER is required")
	}

	hostname = utils.NormalizeHostname(hostname)
	if hostname == "" {
		return errors.New("hostname is required")
	}
	if !hostnameMatchesBaseDomain(hostname, m.cfg.BaseDomain) {
		return fmt.Errorf("hostname %q is outside acme base domain %q", hostname, m.cfg.BaseDomain)
	}

	address, err := utils.NormalizeEVMAddress(address)
	if err != nil {
		return fmt.Errorf("normalize ens gasless address: %w", err)
	}
	return m.dns.EnsureTXTRecord(ctx, hostname, gaslessENSTXTPrefix+defaultENSGaslessResolver+" "+strings.TrimSpace(address))
}

func (m *Manager) DeleteENSGaslessHostname(ctx context.Context, hostname string) error {
	if m == nil || !m.cfg.ENSGaslessEnabled || utils.IsLocalRelayHost(m.cfg.BaseDomain) {
		return nil
	}
	if m.dns == nil {
		return errors.New("ACME_DNS_PROVIDER is required")
	}

	hostname = utils.NormalizeHostname(hostname)
	if hostname == "" {
		return nil
	}
	if !hostnameMatchesBaseDomain(hostname, m.cfg.BaseDomain) {
		return nil
	}
	return m.dns.DeleteTXTRecords(ctx, hostname, gaslessENSTXTPrefix)
}

func hostnameMatchesBaseDomain(hostname, baseDomain string) bool {
	hostname = utils.NormalizeHostname(hostname)
	baseDomain = strings.TrimPrefix(utils.NormalizeHostname(baseDomain), "*.")
	if hostname == "" || baseDomain == "" {
		return false
	}
	return hostname == baseDomain || strings.HasSuffix(hostname, "."+baseDomain)
}

func (m *Manager) shouldRenew() bool {
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
	cert, err := utils.ParseCertificatePEM(certPEM)
	if err != nil {
		return false, err
	}
	if time.Until(cert.NotAfter) < 30*24*time.Hour {
		return true, nil
	}
	return !certificateCoversDomains(cert, domains), nil
}

func certCoversDomains(certFile string, domains []string) (bool, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return false, err
	}
	cert, err := utils.ParseCertificatePEM(certPEM)
	if err != nil {
		return false, err
	}
	return certificateCoversDomains(cert, domains), nil
}

func certificateCoversDomains(cert *x509.Certificate, domains []string) bool {
	for _, domain := range domains {
		if wildcardDomain, ok := strings.CutPrefix(domain, "*."); ok {
			if !certificateCoversHostname(cert, "probe."+wildcardDomain) {
				return false
			}
			continue
		}
		if !certificateCoversHostname(cert, domain) {
			return false
		}
	}
	return true
}

func certificateCoversHostname(cert *x509.Certificate, hostname string) bool {
	return cert != nil && cert.VerifyHostname(hostname) == nil
}

func newClient(ctx context.Context, email, accountKeyFile, registrationFile string, dnsProvider DNSProvider) (*lego.Client, error) {
	accountKey, err := loadOrCreateAccountKey(accountKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load acme account key: %w", err)
	}
	accountReg, err := loadRegistration(registrationFile)
	if err != nil {
		return nil, fmt.Errorf("load acme registration: %w", err)
	}

	user := &acmeUser{
		Email:        email,
		Key:          accountKey,
		Registration: accountReg,
	}

	clientConfig := lego.NewConfig(user)
	clientConfig.CADirURL = lego.LEDirectoryProduction
	clientConfig.Certificate.KeyType = certcrypto.RSA2048

	client, err := lego.NewClient(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("create acme client: %w", err)
	}

	if dnsProvider == nil {
		return nil, errors.New("ACME_DNS_PROVIDER is required")
	}
	challengeProvider, err := dnsProvider.ChallengeProvider(ctx)
	if err != nil {
		return nil, fmt.Errorf("create dns challenge provider: %w", err)
	}
	if err := client.Challenge.SetDNS01Provider(challengeProvider); err != nil {
		return nil, fmt.Errorf("set dns01 provider: %w", err)
	}

	if user.Registration == nil {
		reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, fmt.Errorf("register acme account: %w", err)
		}
		user.Registration = reg
		if err := saveRegistration(registrationFile, reg); err != nil {
			return nil, fmt.Errorf("persist acme registration: %w", err)
		}
	}

	return client, nil
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
