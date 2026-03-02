package acme

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// DefaultCacheTTL is the default time-to-live for cached certificates.
	DefaultCacheTTL = 24 * time.Hour

	// ExpiryThreshold is how long before cert expiration to refresh cache.
	ExpiryThreshold = 30 * 24 * time.Hour

	// DefaultCacheFileName is the default filename for certificate cache.
	DefaultCacheFileName = "portal-cert-cache.json"
)

// DefaultCertCachePath returns the default path for certificate cache.
// It uses the OS-specific user cache directory if available, otherwise
// falls back to the current directory.
func DefaultCertCachePath() string {
	if cacheDir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cacheDir, "portal", DefaultCacheFileName)
	}
	if homeDir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(homeDir, ".cache", "portal", DefaultCacheFileName)
	}
	return DefaultCacheFileName
}

// CertCacheEntry represents a cached certificate chain.
type CertCacheEntry struct {
	CertPEM   []byte `json:"cert_pem"`
	RootCAPEM []byte `json:"root_ca_pem"`
	FetchedAt int64  `json:"fetched_at"`
}

// ParseCertificatePEM parses a PEM-encoded certificate.
func ParseCertificatePEM(pemData []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	return x509.ParseCertificate(block.Bytes)
}

// LoadCertCache loads a certificate cache from file.
func LoadCertCache(path string) (*CertCacheEntry, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cert cache: %w", err)
	}
	var entry CertCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("parse cert cache: %w", err)
	}
	return &entry, nil
}

// SaveCertCache saves a certificate cache to file.
func SaveCertCache(path string, entry *CertCacheEntry) error {
	if path == "" || entry == nil {
		return nil
	}

	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create cert cache directory: %w", err)
		}
	}

	toSave := *entry
	toSave.FetchedAt = time.Now().Unix()

	data, err := json.Marshal(&toSave)
	if err != nil {
		return fmt.Errorf("marshal cert cache: %w", err)
	}
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		return fmt.Errorf("write cert cache: %w", err)
	}
	return nil
}

// IsCertCacheFresh checks if a cached certificate is still fresh.
// It checks both the TTL (time since fetch) and the certificate expiration.
func IsCertCacheFresh(entry *CertCacheEntry, ttl time.Duration) bool {
	if entry == nil || len(entry.CertPEM) == 0 {
		return false
	}

	// Check TTL (how long since we fetched)
	fetchedAt := time.Unix(entry.FetchedAt, 0)
	if time.Since(fetchedAt) >= ttl {
		return false
	}

	// Check cert expiration (in case ACME renewed on relay side)
	cert, err := ParseCertificatePEM(entry.CertPEM)
	if err != nil {
		return false
	}
	timeUntilExpiry := time.Until(cert.NotAfter)
	if timeUntilExpiry < ExpiryThreshold {
		log.Debug().
			Time("not_after", cert.NotAfter).
			Dur("time_remaining", timeUntilExpiry).
			Msg("[certcache] Cached certificate is close to expiration, will refresh")
		return false
	}

	return true
}
