package manager

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	maxFailedAttempts      = 3
	lockDuration           = 1 * time.Minute
	sessionDuration        = 24 * time.Hour
	failedLoginRetention   = 15 * time.Minute
	failedLoginSweepWindow = 1 * time.Minute
	maxFailedLoginEntries  = 4096
)

// AuthManager manages admin authentication with rate limiting.
type AuthManager struct {
	lastSweepAt  time.Time
	failedLogins map[string]*loginAttempt
	sessions     map[string]time.Time
	secretKey    string
	mu           sync.RWMutex
}

type loginAttempt struct {
	lockedAt   time.Time
	lastSeenAt time.Time
	count      int
}

// NewAuthManager creates a new AuthManager with the given secret key.
func NewAuthManager(secretKey string) *AuthManager {
	// Create AuthManager for admin authentication
	// Auto-generate secret key if not provided
	if secretKey == "" {
		randomBytes := make([]byte, 16)
		if _, err := rand.Read(randomBytes); err != nil {
			log.Fatal().Err(err).Msg("[server] failed to generate random admin secret key")
		}
		secretKey = hex.EncodeToString(randomBytes)
		log.Warn().Int("key_length", len(secretKey)).Msg("[server] auto-generated ADMIN_SECRET_KEY (set ADMIN_SECRET_KEY env to use your own)")
	} else {
		log.Info().Int("key_length", len(secretKey)).Msg("[server] admin authentication enabled")
	}

	return &AuthManager{
		secretKey:    secretKey,
		failedLogins: make(map[string]*loginAttempt),
		sessions:     make(map[string]time.Time),
	}
}

// IsIPLocked checks if an IP is currently locked out.
func (m *AuthManager) IsIPLocked(ip string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	attempt, exists := m.failedLogins[ip]
	if !exists {
		return false
	}
	return lockRemaining(attempt, time.Now()) > 0
}

// GetLockRemainingSeconds returns the remaining seconds until the IP is unlocked.
func (m *AuthManager) GetLockRemainingSeconds(ip string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	attempt, exists := m.failedLogins[ip]
	if !exists {
		return 0
	}
	return int(lockRemaining(attempt, time.Now()).Seconds())
}

// RecordFailedLogin records a failed login attempt and returns true if the IP is now locked.
func (m *AuthManager) RecordFailedLogin(ip string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	m.maybeSweepFailedLoginsLocked(now)

	attempt, exists := m.failedLogins[ip]
	if !exists {
		attempt = &loginAttempt{}
		m.failedLogins[ip] = attempt
	}

	// Reset if lock has expired
	if attempt.count >= maxFailedAttempts && now.Sub(attempt.lockedAt) >= lockDuration {
		attempt.count = 0
	}

	attempt.count++
	attempt.lastSeenAt = now

	locked := false
	if attempt.count >= maxFailedAttempts {
		attempt.lockedAt = now
		locked = true
	}

	m.enforceFailedLoginCapLocked()
	return locked
}

// ResetFailedLogin resets the failed login count for an IP.
func (m *AuthManager) ResetFailedLogin(ip string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.failedLogins, ip)
}

// ValidateKey checks if the provided key matches the secret key.
func (m *AuthManager) ValidateKey(key string) bool {
	if m.secretKey == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(key), []byte(m.secretKey)) == 1
}

// HasSecretKey returns true if a secret key is configured.
func (m *AuthManager) HasSecretKey() bool {
	return m.secretKey != ""
}

// CreateSession creates a new session and returns the token.
func (m *AuthManager) CreateSession() string {
	token, err := generateToken()
	if err != nil {
		log.Fatal().Err(err).Msg("[server] failed to generate secure admin session token")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.sessions[token] = time.Now().Add(sessionDuration)

	// Clean up expired sessions
	m.cleanupExpiredSessions()

	return token
}

// ValidateSession checks if a session token is valid.
func (m *AuthManager) ValidateSession(token string) bool {
	if token == "" {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	expiry, exists := m.sessions[token]
	if !exists {
		return false
	}

	return time.Now().Before(expiry)
}

// DeleteSession removes a session.
func (m *AuthManager) DeleteSession(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.sessions, token)
}

// cleanupExpiredSessions removes expired sessions (must be called with lock held).
func (m *AuthManager) cleanupExpiredSessions() {
	now := time.Now()
	for token, expiry := range m.sessions {
		if now.After(expiry) {
			delete(m.sessions, token)
		}
	}
}

// generateToken generates a secure random token.
func generateToken() (string, error) {
	return generateTokenFromReader(rand.Reader)
}

func generateTokenFromReader(reader io.Reader) (string, error) {
	bytes := make([]byte, 32)
	if _, err := io.ReadFull(reader, bytes); err != nil {
		return "", fmt.Errorf("read random session token bytes: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func (m *AuthManager) maybeSweepFailedLoginsLocked(now time.Time) {
	if !m.lastSweepAt.IsZero() && now.Sub(m.lastSweepAt) < failedLoginSweepWindow {
		return
	}

	m.sweepExpiredFailedLoginsLocked(now)
	m.lastSweepAt = now
}

func (m *AuthManager) sweepExpiredFailedLoginsLocked(now time.Time) {
	for ip, attempt := range m.failedLogins {
		if attempt == nil {
			delete(m.failedLogins, ip)
			continue
		}

		lastSeenAt := attempt.lastSeenAt
		if lastSeenAt.IsZero() {
			lastSeenAt = attempt.lockedAt
		}
		if lastSeenAt.IsZero() || now.Sub(lastSeenAt) >= failedLoginRetention {
			delete(m.failedLogins, ip)
		}
	}
}

func (m *AuthManager) enforceFailedLoginCapLocked() {
	if len(m.failedLogins) <= maxFailedLoginEntries {
		return
	}

	type failedEntry struct {
		lastSeenAt time.Time
		ip         string
	}

	entries := make([]failedEntry, 0, len(m.failedLogins))
	for ip, attempt := range m.failedLogins {
		if attempt == nil {
			entries = append(entries, failedEntry{ip: ip})
			continue
		}

		lastSeenAt := attempt.lastSeenAt
		if lastSeenAt.IsZero() {
			lastSeenAt = attempt.lockedAt
		}

		entries = append(entries, failedEntry{
			ip:         ip,
			lastSeenAt: lastSeenAt,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].lastSeenAt.Before(entries[j].lastSeenAt)
	})

	overflow := len(m.failedLogins) - maxFailedLoginEntries
	for i := range overflow {
		delete(m.failedLogins, entries[i].ip)
	}
}

func lockRemaining(attempt *loginAttempt, now time.Time) time.Duration {
	if attempt == nil || attempt.count < maxFailedAttempts {
		return 0
	}

	remaining := lockDuration - now.Sub(attempt.lockedAt)
	if remaining <= 0 {
		return 0
	}

	return remaining
}
