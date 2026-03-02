package acme

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	lego "github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/rs/zerolog/log"
)

const (
	// RenewalCheckInterval is how often to check if renewal is needed.
	RenewalCheckInterval = 24 * time.Hour

	// RenewalThreshold is how long before expiration to renew.
	RenewalThreshold = 30 * 24 * time.Hour

	// RenewalOperationTimeout bounds a single renewal attempt.
	RenewalOperationTimeout = 2 * time.Minute
)

// Start begins the certificate renewal loop. It checks periodically if the
// certificate needs renewal and renews it automatically.
func (m *AcmeManager) Start(ctx context.Context) {
	if m == nil || m.cfg.KeyDir == "" || !hasCloudflareToken(m.cfg.CloudflareToken) {
		return
	}

	m.startOnce.Do(func() {
		m.waitGroup.Add(1)
		go m.renewalLoop(ctx)
	})
}

// Stop stops the renewal loop.
func (m *AcmeManager) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	m.waitGroup.Wait()
}

func (m *AcmeManager) renewalLoop(ctx context.Context) {
	defer m.waitGroup.Done()

	ticker := time.NewTicker(RenewalCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if m.shouldRenew() {
				renewCtx, cancel := context.WithTimeout(ctx, RenewalOperationTimeout)
				err := m.renewCertificate(renewCtx)
				cancel()
				if err != nil {
					log.Error().Err(err).Msg("[acme] certificate renewal failed")
				}
			}
		}
	}
}

// shouldRenew checks if the certificate needs renewal.
func (m *AcmeManager) shouldRenew() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	targets, err := buildCertTargets(m.cfg.BaseDomain, m.cfg.KeyDir)
	if err != nil {
		log.Debug().Err(err).Msg("[acme] cannot build certificate targets for renewal check")
		return false
	}

	for _, target := range targets {
		needsRenewal, checkErr := certNeedsRenewal(target.CertFile)
		if checkErr != nil {
			log.Debug().
				Err(checkErr).
				Str("target", target.Name).
				Str("cert_file", target.CertFile).
				Msg("[acme] cannot read certificate for renewal check")
			continue
		}
		if needsRenewal {
			return true
		}
	}
	return false
}

func certNeedsRenewal(certFile string) (bool, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return false, err
	}

	cert, err := ParseCertificatePEM(certPEM)
	if err != nil {
		return false, err
	}

	timeUntilExpiry := time.Until(cert.NotAfter)
	needsRenewal := timeUntilExpiry < RenewalThreshold
	if needsRenewal {
		log.Info().
			Time("not_after", cert.NotAfter).
			Dur("time_remaining", timeUntilExpiry).
			Str("cert_file", certFile).
			Msg("[acme] certificate needs renewal")
	} else {
		log.Debug().
			Time("not_after", cert.NotAfter).
			Dur("time_remaining", timeUntilExpiry).
			Str("cert_file", certFile).
			Msg("[acme] certificate does not need renewal")
	}
	return needsRenewal, nil
}

// renewCertificate renews the certificate via ACME.
func (m *AcmeManager) renewCertificate(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("renewal canceled: %w", err)
	}

	if m.cfg.BaseDomain == "" {
		return errors.New("base domain not configured")
	}

	targets, err := buildCertTargets(m.cfg.BaseDomain, m.cfg.KeyDir)
	if err != nil {
		return fmt.Errorf("build ACME targets: %w", err)
	}

	for _, target := range targets {
		needsRenewal, checkErr := certNeedsRenewal(target.CertFile)
		if checkErr != nil {
			continue
		}
		if !needsRenewal {
			continue
		}

		cfg, cfgErr := buildProvisionConfig(m.cfg.BaseDomain, target, m.cfg.CloudflareToken)
		if cfgErr != nil {
			return fmt.Errorf("build provision config: %w", cfgErr)
		}

		log.Info().
			Str("target", cfg.TargetName).
			Strs("domains", cfg.Domains).
			Str("cert_file", cfg.CertFile).
			Msg("[acme] renewing certificate")

		if err := m.doRenew(cfg); err != nil {
			return fmt.Errorf("renew certificate for target %s: %w", cfg.TargetName, err)
		}

		log.Info().
			Str("target", cfg.TargetName).
			Str("cert_file", cfg.CertFile).
			Msg("[acme] certificate renewed successfully")
	}
	return nil
}

func (m *AcmeManager) doRenew(cfg provisionConfig) error {
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

	certFile := cfg.CertFile
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return fmt.Errorf("read certificate for renewal: %w", err)
	}

	renewed, err := client.Certificate.Renew(certificate.Resource{
		Domain:      cfg.Domains[0],
		Certificate: certPEM,
		PrivateKey:  nil,
	}, true, false, "")
	if err != nil {
		return fmt.Errorf("ACME renew: %w", err)
	}
	if len(renewed.Certificate) == 0 {
		return errors.New("ACME renewal response did not include certificate chain")
	}

	if err := writeFileAtomic(cfg.CertFile, renewed.Certificate, 0o644); err != nil {
		return fmt.Errorf("write renewed certificate chain: %w", err)
	}

	return nil
}
