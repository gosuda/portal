package policy

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type failingReader struct{}

func (failingReader) Read(_ []byte) (int, error) {
	return 0, errors.New("rng unavailable")
}

func TestGenerateTokenFromReaderFailsClosed(t *testing.T) {
	t.Parallel()

	token, err := generateTokenFromReader(failingReader{})
	if err == nil {
		t.Fatal("expected an error when entropy source fails")
	}
	if token != "" {
		t.Fatalf("expected empty token on entropy failure, got %q", token)
	}
}

func TestRecordFailedLoginSweepsExpiredEntries(t *testing.T) {
	t.Parallel()

	m := NewAuthenticator("test-secret")
	now := time.Now()

	m.mu.Lock()
	m.failedLogins["expired-entry"] = &loginAttempt{
		count:      1,
		lastSeenAt: now.Add(-failedLoginRetention - time.Second),
	}
	m.lastSweepAt = now.Add(-failedLoginSweepWindow - time.Second)
	m.mu.Unlock()

	locked := m.RecordFailedLogin("active-entry")
	if locked {
		t.Fatal("first failed login attempt should not lock the IP")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.failedLogins) != 1 {
		t.Fatalf("expected 1 retained entry after sweep, got %d", len(m.failedLogins))
	}
	if _, exists := m.failedLogins["expired-entry"]; exists {
		t.Fatal("expired failed login entry should have been removed")
	}
	if _, exists := m.failedLogins["active-entry"]; !exists {
		t.Fatal("active failed login entry should be retained")
	}
}

func TestRecordFailedLoginEnforcesEntryCap(t *testing.T) {
	t.Parallel()

	m := NewAuthenticator("test-secret")
	base := time.Now().Add(-2 * time.Minute)

	m.mu.Lock()
	for i := range maxFailedLoginEntries {
		key := fmt.Sprintf("old-%05d", i)
		m.failedLogins[key] = &loginAttempt{
			count:      1,
			lastSeenAt: base.Add(time.Duration(i) * time.Millisecond),
		}
	}
	m.lastSweepAt = time.Now()
	m.mu.Unlock()

	m.RecordFailedLogin("new-entry")

	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.failedLogins) != maxFailedLoginEntries {
		t.Fatalf("expected failed login map cap of %d, got %d", maxFailedLoginEntries, len(m.failedLogins))
	}
	if _, exists := m.failedLogins["new-entry"]; !exists {
		t.Fatal("new failed login entry should be retained after eviction")
	}
	if _, exists := m.failedLogins["old-00000"]; exists {
		t.Fatal("oldest failed login entry should be evicted when cap is exceeded")
	}
}

func TestAuthenticatorDoesNotLogPlaintextSecretsOrSessionToken(t *testing.T) {
	const secretKey = "super-secret-admin-key"

	var buf bytes.Buffer
	originalLogger := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() {
		log.Logger = originalLogger
	})

	m := NewAuthenticator(secretKey)
	logOutput := buf.String()
	if strings.Contains(logOutput, secretKey) {
		t.Fatalf("expected auth manager logs to omit plaintext secret key, got %q", logOutput)
	}

	buf.Reset()
	token := m.CreateSession()
	if token == "" {
		t.Fatal("expected non-empty session token")
	}
	if strings.Contains(buf.String(), token) {
		t.Fatalf("expected auth manager logs to omit plaintext session token, got %q", buf.String())
	}
}
