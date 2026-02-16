package manager

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"sync"
	"time"
)

const (
	maxFailedAttempts = 3
	lockDuration      = 1 * time.Minute
	sessionDuration   = 24 * time.Hour
)

// AuthManager manages admin authentication with rate limiting.
type AuthManager struct {
	secretKey    string
	mu           sync.RWMutex
	failedLogins map[string]*loginAttempt // IP -> attempt info
	sessions     map[string]time.Time     // token -> expiry
}

type loginAttempt struct {
	count    int
	lockedAt time.Time
}

// NewAuthManager creates a new AuthManager with the given secret key.
func NewAuthManager(secretKey string) *AuthManager {
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

	if attempt.count >= maxFailedAttempts {
		// Check if lock has expired
		if time.Since(attempt.lockedAt) < lockDuration {
			return true
		}
	}

	return false
}

// GetLockRemainingSeconds returns the remaining seconds until the IP is unlocked.
func (m *AuthManager) GetLockRemainingSeconds(ip string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	attempt, exists := m.failedLogins[ip]
	if !exists {
		return 0
	}

	if attempt.count >= maxFailedAttempts {
		remaining := lockDuration - time.Since(attempt.lockedAt)
		if remaining > 0 {
			return int(remaining.Seconds())
		}
	}

	return 0
}

// RecordFailedLogin records a failed login attempt and returns true if the IP is now locked.
func (m *AuthManager) RecordFailedLogin(ip string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	attempt, exists := m.failedLogins[ip]
	if !exists {
		attempt = &loginAttempt{}
		m.failedLogins[ip] = attempt
	}

	// Reset if lock has expired
	if attempt.count >= maxFailedAttempts && time.Since(attempt.lockedAt) >= lockDuration {
		attempt.count = 0
	}

	attempt.count++

	if attempt.count >= maxFailedAttempts {
		attempt.lockedAt = time.Now()
		return true
	}

	return false
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
	token := generateToken()

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
func generateToken() string {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp-based token (less secure but functional)
		return hex.EncodeToString([]byte(time.Now().String()))
	}
	return hex.EncodeToString(bytes)
}
