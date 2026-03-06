package policy

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
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

type Authenticator struct {
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

func NewAuthenticator(secretKey string) *Authenticator {
	secretKey = strings.TrimSpace(secretKey)
	if secretKey == "" {
		generated, err := generateSecretKey()
		if err != nil {
			log.Fatal().Err(err).Msg("generate admin secret key")
		}
		secretKey = generated
		log.Warn().
			Str("component", "portal-admin").
			Str("admin_secret_key", secretKey).
			Msg("generated random admin secret key because ADMIN_SECRET_KEY was empty")
	}

	return &Authenticator{
		secretKey:    secretKey,
		failedLogins: make(map[string]*loginAttempt),
		sessions:     make(map[string]time.Time),
	}
}

func (a *Authenticator) AuthEnabled() bool {
	return a != nil && a.secretKey != ""
}

func (a *Authenticator) ValidateKey(key string) bool {
	if !a.AuthEnabled() {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a.secretKey), []byte(key)) == 1
}

func (a *Authenticator) IsIPLocked(ip string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	attempt := a.failedLogins[ip]
	return lockRemaining(attempt, time.Now()) > 0
}

func (a *Authenticator) LockRemainingSeconds(ip string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return int(lockRemaining(a.failedLogins[ip], time.Now()).Seconds())
}

func (a *Authenticator) RecordFailedLogin(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	a.maybeSweepFailedLoginsLocked(now)

	attempt := a.failedLogins[ip]
	if attempt == nil {
		attempt = &loginAttempt{}
		a.failedLogins[ip] = attempt
	}
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

	a.enforceFailedLoginCapLocked()
	return locked
}

func (a *Authenticator) ResetFailedLogin(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.failedLogins, ip)
}

func (a *Authenticator) CreateSession() (string, error) {
	token, err := generateToken()
	if err != nil {
		return "", err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions[token] = time.Now().Add(sessionDuration)
	a.cleanupExpiredSessionsLocked()
	return token, nil
}

func (a *Authenticator) ValidateSession(token string) bool {
	if token == "" {
		return false
	}

	a.mu.RLock()
	defer a.mu.RUnlock()

	expiry, ok := a.sessions[token]
	return ok && time.Now().Before(expiry)
}

func (a *Authenticator) DeleteSession(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, token)
}

func (a *Authenticator) maybeSweepFailedLoginsLocked(now time.Time) {
	if !a.lastSweepAt.IsZero() && now.Sub(a.lastSweepAt) < failedLoginSweepWindow {
		return
	}
	for ip, attempt := range a.failedLogins {
		lastSeenAt := attempt.lastSeenAt
		if lastSeenAt.IsZero() {
			lastSeenAt = attempt.lockedAt
		}
		if lastSeenAt.IsZero() || now.Sub(lastSeenAt) >= failedLoginRetention {
			delete(a.failedLogins, ip)
		}
	}
	a.lastSweepAt = now
}

func (a *Authenticator) enforceFailedLoginCapLocked() {
	if len(a.failedLogins) <= maxFailedLoginEntries {
		return
	}

	type failedEntry struct {
		ip         string
		lastSeenAt time.Time
	}

	entries := make([]failedEntry, 0, len(a.failedLogins))
	for ip, attempt := range a.failedLogins {
		lastSeenAt := attempt.lastSeenAt
		if lastSeenAt.IsZero() {
			lastSeenAt = attempt.lockedAt
		}
		entries = append(entries, failedEntry{ip: ip, lastSeenAt: lastSeenAt})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].lastSeenAt.Before(entries[j].lastSeenAt)
	})

	for i := range len(a.failedLogins) - maxFailedLoginEntries {
		delete(a.failedLogins, entries[i].ip)
	}
}

func (a *Authenticator) cleanupExpiredSessionsLocked() {
	now := time.Now()
	for token, expiry := range a.sessions {
		if now.After(expiry) {
			delete(a.sessions, token)
		}
	}
}

func generateToken() (string, error) {
	return generateTokenFromReader(rand.Reader)
}

func generateSecretKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("read random admin secret key bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func generateTokenFromReader(reader io.Reader) (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(reader, buf); err != nil {
		return "", fmt.Errorf("read random session token bytes: %w", err)
	}
	return hex.EncodeToString(buf), nil
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
