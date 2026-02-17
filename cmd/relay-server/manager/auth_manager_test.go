package manager

import (
	"fmt"
	"sync"
	"testing"
	"time"
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
			if got := m.HasSecretKey(); got != tt.wantSecret {
				t.Fatalf("HasSecretKey() = %v, want %v", got, tt.wantSecret)
			}
			if got := m.ValidateKey(tt.key); got != tt.wantValid {
				t.Fatalf("ValidateKey(%q) = %v, want %v", tt.key, got, tt.wantValid)
			}
		})
	}
}

func TestAuthManagerFailedLoginLockAndReset(t *testing.T) {
	t.Parallel()

	const ip = "203.0.113.42"
	m := NewAuthManager("admin-secret")

	if locked := m.RecordFailedLogin(ip); locked {
		t.Fatal("first failed login unexpectedly locked IP")
	}
	if locked := m.RecordFailedLogin(ip); locked {
		t.Fatal("second failed login unexpectedly locked IP")
	}
	if m.IsIPLocked(ip) {
		t.Fatal("IP should not be locked before max failed attempts")
	}
	if remaining := m.GetLockRemainingSeconds(ip); remaining != 0 {
		t.Fatalf("GetLockRemainingSeconds() = %d, want 0 before lock", remaining)
	}

	if locked := m.RecordFailedLogin(ip); !locked {
		t.Fatal("third failed login should lock IP")
	}
	if !m.IsIPLocked(ip) {
		t.Fatal("IsIPLocked() = false, want true after lock")
	}

	lockRemaining := m.GetLockRemainingSeconds(ip)
	maxSeconds := int(lockDuration.Seconds())
	if lockRemaining <= 0 || lockRemaining > maxSeconds {
		t.Fatalf("GetLockRemainingSeconds() = %d, want in range [1,%d]", lockRemaining, maxSeconds)
	}

	m.ResetFailedLogin(ip)
	if m.IsIPLocked(ip) {
		t.Fatal("IP should not be locked after ResetFailedLogin")
	}
	if remaining := m.GetLockRemainingSeconds(ip); remaining != 0 {
		t.Fatalf("GetLockRemainingSeconds() = %d, want 0 after reset", remaining)
	}
}

func TestAuthManagerFailedLoginLockExpires(t *testing.T) {
	t.Parallel()

	const ip = "203.0.113.50"
	m := NewAuthManager("admin-secret")

	for range maxFailedAttempts {
		m.RecordFailedLogin(ip)
	}
	if !m.IsIPLocked(ip) {
		t.Fatal("IP should be locked after max failed attempts")
	}

	m.mu.Lock()
	attempt := m.failedLogins[ip]
	if attempt == nil {
		m.mu.Unlock()
		t.Fatal("missing failed login state for locked IP")
	}
	attempt.lockedAt = time.Now().Add(-lockDuration - time.Second)
	m.mu.Unlock()

	if m.IsIPLocked(ip) {
		t.Fatal("IP should be unlocked after lock duration elapses")
	}
	if remaining := m.GetLockRemainingSeconds(ip); remaining != 0 {
		t.Fatalf("GetLockRemainingSeconds() = %d, want 0 after expiry", remaining)
	}

	if locked := m.RecordFailedLogin(ip); locked {
		t.Fatal("first failed login after lock expiry should not re-lock immediately")
	}
	if m.IsIPLocked(ip) {
		t.Fatal("IP should remain unlocked after first post-expiry failure")
	}
}

func TestAuthManagerSessionLifecycle(t *testing.T) {
	t.Parallel()

	m := NewAuthManager("admin-secret")

	if m.ValidateSession("") {
		t.Fatal("ValidateSession(\"\") = true, want false")
	}
	if m.ValidateSession("missing-token") {
		t.Fatal("ValidateSession(missing) = true, want false")
	}

	token := m.CreateSession()
	if token == "" {
		t.Fatal("CreateSession() returned empty token")
	}
	if !m.ValidateSession(token) {
		t.Fatal("new session token should validate")
	}

	m.mu.Lock()
	m.sessions["expired-token"] = time.Now().Add(-time.Second)
	m.mu.Unlock()
	if m.ValidateSession("expired-token") {
		t.Fatal("expired session token should not validate")
	}

	m.DeleteSession(token)
	if m.ValidateSession(token) {
		t.Fatal("deleted session token should not validate")
	}

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
	if newToken == "" {
		t.Fatal("CreateSession() returned empty token")
	}

	m.mu.RLock()
	_, expiredExists := m.sessions["expired"]
	_, newExists := m.sessions[newToken]
	m.mu.RUnlock()

	if expiredExists {
		t.Fatal("expired session was not cleaned up")
	}
	if !newExists {
		t.Fatal("new session missing after CreateSession")
	}
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
		if token == "" {
			t.Fatal("CreateSession() returned empty token in concurrent path")
		}
		if _, exists := seen[token]; exists {
			t.Fatalf("duplicate session token generated: %q", token)
		}
		seen[token] = struct{}{}
		if !m.ValidateSession(token) {
			t.Fatalf("session token %q failed validation", token)
		}
	}

	if len(seen) != workers {
		t.Fatalf("unique token count = %d, want %d", len(seen), workers)
	}
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
		if !m.IsIPLocked(ip) {
			t.Fatalf("expected %s to be locked after 3 failed logins", ip)
		}
	}
}
