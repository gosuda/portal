package manager

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAuthManagerValidateKeyAndSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		secret     string
		key        string
		wantValid  bool
		wantSecret bool
	}{
		{
			name:       "matching key",
			secret:     "admin-secret",
			key:        "admin-secret",
			wantValid:  true,
			wantSecret: true,
		},
		{
			name:       "non-matching key",
			secret:     "admin-secret",
			key:        "wrong",
			wantValid:  false,
			wantSecret: true,
		},
		{
			name:       "empty input key",
			secret:     "admin-secret",
			key:        "",
			wantValid:  false,
			wantSecret: true,
		},
		{
			name:       "no configured secret",
			secret:     "",
			key:        "any",
			wantValid:  false,
			wantSecret: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := NewAuthManager(tt.secret)
			require.Equal(t, tt.wantSecret, m.HasSecretKey(), "HasSecretKey() mismatch")
			require.Equal(t, tt.wantValid, m.ValidateKey(tt.key), "ValidateKey(%q) mismatch", tt.key)
		})
	}
}

func TestAuthManagerFailedLoginLockAndReset(t *testing.T) {
	t.Parallel()

	const ip = "203.0.113.42"
	m := NewAuthManager("admin-secret")

	require.False(t, m.RecordFailedLogin(ip), "first failed login should not lock IP")
	require.False(t, m.RecordFailedLogin(ip), "second failed login should not lock IP")
	require.False(t, m.IsIPLocked(ip), "IP should not be locked before max failed attempts")
	require.Zero(t, m.GetLockRemainingSeconds(ip), "GetLockRemainingSeconds() should be 0 before lock")

	require.True(t, m.RecordFailedLogin(ip), "third failed login should lock IP")
	require.True(t, m.IsIPLocked(ip), "IsIPLocked() should be true after lock")

	lockRemaining := m.GetLockRemainingSeconds(ip)
	maxSeconds := int(lockDuration.Seconds())
	require.Greater(t, lockRemaining, 0, "GetLockRemainingSeconds() should be > 0")
	require.LessOrEqual(t, lockRemaining, maxSeconds, "GetLockRemainingSeconds() should be <= %d", maxSeconds)

	m.ResetFailedLogin(ip)
	require.False(t, m.IsIPLocked(ip), "IP should not be locked after ResetFailedLogin")
	require.Zero(t, m.GetLockRemainingSeconds(ip), "GetLockRemainingSeconds() should be 0 after reset")
}

func TestAuthManagerFailedLoginLockExpires(t *testing.T) {
	t.Parallel()

	const ip = "203.0.113.50"
	m := NewAuthManager("admin-secret")

	for range maxFailedAttempts {
		m.RecordFailedLogin(ip)
	}
	require.True(t, m.IsIPLocked(ip), "IP should be locked after max failed attempts")

	m.mu.Lock()
	attempt := m.failedLogins[ip]
	require.NotNil(t, attempt, "missing failed login state for locked IP")
	attempt.lockedAt = time.Now().Add(-lockDuration - time.Second)
	m.mu.Unlock()

	require.False(t, m.IsIPLocked(ip), "IP should be unlocked after lock duration elapses")
	require.Zero(t, m.GetLockRemainingSeconds(ip), "GetLockRemainingSeconds() should be 0 after expiry")

	require.False(t, m.RecordFailedLogin(ip), "first failed login after lock expiry should not re-lock immediately")
	require.False(t, m.IsIPLocked(ip), "IP should remain unlocked after first post-expiry failure")
}

func TestAuthManagerSessionLifecycle(t *testing.T) {
	t.Parallel()

	m := NewAuthManager("admin-secret")

	require.False(t, m.ValidateSession(""), "ValidateSession(\"\") should be false")
	require.False(t, m.ValidateSession("missing-token"), "ValidateSession(missing) should be false")

	token := m.CreateSession()
	require.NotEmpty(t, token, "CreateSession() should return non-empty token")
	require.True(t, m.ValidateSession(token), "new session token should validate")

	m.mu.Lock()
	m.sessions["expired-token"] = time.Now().Add(-time.Second)
	m.mu.Unlock()
	require.False(t, m.ValidateSession("expired-token"), "expired session token should not validate")

	m.DeleteSession(token)
	require.False(t, m.ValidateSession(token), "deleted session token should not validate")

	// No-op path.
	m.DeleteSession("")
}

func TestAuthManagerCreateSessionCleansExpiredSessions(t *testing.T) {
	t.Parallel()

	m := NewAuthManager("admin-secret")
	m.mu.Lock()
	m.sessions["expired"] = time.Now().Add(-2 * time.Hour)
	m.mu.Unlock()

	newToken := m.CreateSession()
	require.NotEmpty(t, newToken, "CreateSession() should return non-empty token")

	m.mu.RLock()
	_, expiredExists := m.sessions["expired"]
	_, newExists := m.sessions[newToken]
	m.mu.RUnlock()

	require.False(t, expiredExists, "expired session should be cleaned up")
	require.True(t, newExists, "new session should exist after CreateSession")
}

func TestAuthManagerConcurrentSessionCreation(t *testing.T) {
	t.Parallel()

	const workers = 48
	m := NewAuthManager("admin-secret")

	var wg sync.WaitGroup
	tokens := make(chan string, workers)

	for range workers {
		wg.Go(func() {
			tokens <- m.CreateSession()
		})
	}

	wg.Wait()
	close(tokens)

	seen := make(map[string]struct{}, workers)
	for token := range tokens {
		require.NotEmpty(t, token, "CreateSession() should return non-empty token in concurrent path")
		require.NotContains(t, seen, token, "duplicate session token generated: %q", token)
		seen[token] = struct{}{}
		require.True(t, m.ValidateSession(token), "session token %q should validate", token)
	}

	require.Len(t, seen, workers, "unique token count mismatch")
}

func TestAuthManagerConcurrentFailedLoginTracking(t *testing.T) {
	t.Parallel()

	const workers = 16
	m := NewAuthManager("admin-secret")

	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		ip := fmt.Sprintf("198.51.100.%d", i+1)
		go func(addr string) {
			defer wg.Done()
			_ = m.RecordFailedLogin(addr)
			_ = m.RecordFailedLogin(addr)
			_ = m.RecordFailedLogin(addr)
		}(ip)
	}
	wg.Wait()

	for i := range workers {
		ip := fmt.Sprintf("198.51.100.%d", i+1)
		require.True(t, m.IsIPLocked(ip), "expected %s to be locked after 3 failed logins", ip)
	}
}
