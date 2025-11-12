package portal

import (
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// UDPSession represents a UDP relay session between two clients
type UDPSession struct {
	SessionToken [UDPSessionTokenSize]byte
	LeaseID      string

	// Client endpoints
	ClientAAddr  *net.UDPAddr
	ClientBAddr  *net.UDPAddr

	// Last activity timestamps
	LastSeenA time.Time
	LastSeenB time.Time

	// Session metadata
	CreatedAt time.Time
	ExpiresAt time.Time
}

// IsExpired checks if the session has expired
func (s *UDPSession) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

// IsActive checks if the session has recent activity
func (s *UDPSession) IsActive(timeout time.Duration) bool {
	now := time.Now()
	return now.Sub(s.LastSeenA) < timeout || now.Sub(s.LastSeenB) < timeout
}

// UpdateActivity updates the last seen time for a client
func (s *UDPSession) UpdateActivity(isClientA bool) {
	if isClientA {
		s.LastSeenA = time.Now()
	} else {
		s.LastSeenB = time.Now()
	}
}

// UDPSessionManager manages UDP sessions for the relay server
type UDPSessionManager struct {
	sessions     map[[UDPSessionTokenSize]byte]*UDPSession
	sessionsLock sync.RWMutex

	// Lease to session mapping for quick lookup
	leaseToSession     map[string][UDPSessionTokenSize]byte
	leaseToSessionLock sync.RWMutex

	stopCh           chan struct{}
	cleanupInterval  time.Duration
	sessionTimeout   time.Duration
	sessionTTL       time.Duration
}

// NewUDPSessionManager creates a new UDP session manager
func NewUDPSessionManager(cleanupInterval, sessionTimeout, sessionTTL time.Duration) *UDPSessionManager {
	return &UDPSessionManager{
		sessions:         make(map[[UDPSessionTokenSize]byte]*UDPSession),
		leaseToSession:   make(map[string][UDPSessionTokenSize]byte),
		stopCh:           make(chan struct{}),
		cleanupInterval:  cleanupInterval,
		sessionTimeout:   sessionTimeout,
		sessionTTL:       sessionTTL,
	}
}

// Start begins the cleanup worker
func (m *UDPSessionManager) Start() {
	go m.cleanupWorker()
}

// Stop stops the cleanup worker
func (m *UDPSessionManager) Stop() {
	close(m.stopCh)
}

// cleanupWorker periodically removes expired sessions
func (m *UDPSessionManager) cleanupWorker() {
	ticker := time.NewTicker(m.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			log.Debug().Msg("[UDPSessionManager] Cleanup worker stopped")
			return
		case <-ticker.C:
			m.cleanupExpiredSessions()
		}
	}
}

// cleanupExpiredSessions removes expired and inactive sessions
func (m *UDPSessionManager) cleanupExpiredSessions() {
	m.sessionsLock.Lock()
	defer m.sessionsLock.Unlock()

	var expiredTokens [][UDPSessionTokenSize]byte

	for token, session := range m.sessions {
		if session.IsExpired() || !session.IsActive(m.sessionTimeout) {
			expiredTokens = append(expiredTokens, token)
		}
	}

	for _, token := range expiredTokens {
		session := m.sessions[token]
		delete(m.sessions, token)

		// Remove lease mapping
		m.leaseToSessionLock.Lock()
		delete(m.leaseToSession, session.LeaseID)
		m.leaseToSessionLock.Unlock()

		log.Debug().
			Str("lease_id", session.LeaseID).
			Str("session_token", SessionTokenToString(token)).
			Msg("[UDPSessionManager] Session cleaned up")
	}

	if len(expiredTokens) > 0 {
		log.Debug().
			Int("cleaned", len(expiredTokens)).
			Int("active", len(m.sessions)).
			Msg("[UDPSessionManager] Cleanup completed")
	}
}

// CreateSession creates a new UDP session for a lease
func (m *UDPSessionManager) CreateSession(leaseID string) (*UDPSession, error) {
	// Generate session token
	token, err := GenerateSessionToken()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	session := &UDPSession{
		SessionToken: token,
		LeaseID:      leaseID,
		CreatedAt:    now,
		ExpiresAt:    now.Add(m.sessionTTL),
		LastSeenA:    now,
		LastSeenB:    now,
	}

	m.sessionsLock.Lock()
	m.sessions[token] = session
	m.sessionsLock.Unlock()

	m.leaseToSessionLock.Lock()
	m.leaseToSession[leaseID] = token
	m.leaseToSessionLock.Unlock()

	log.Debug().
		Str("lease_id", leaseID).
		Str("session_token", SessionTokenToString(token)).
		Msg("[UDPSessionManager] New session created")

	return session, nil
}

// GetSession retrieves a session by token
func (m *UDPSessionManager) GetSession(token [UDPSessionTokenSize]byte) (*UDPSession, bool) {
	m.sessionsLock.RLock()
	defer m.sessionsLock.RUnlock()

	session, exists := m.sessions[token]
	if !exists {
		return nil, false
	}

	// Check if expired
	if session.IsExpired() {
		return nil, false
	}

	return session, true
}

// GetSessionByLease retrieves a session by lease ID
func (m *UDPSessionManager) GetSessionByLease(leaseID string) (*UDPSession, bool) {
	m.leaseToSessionLock.RLock()
	token, exists := m.leaseToSession[leaseID]
	m.leaseToSessionLock.RUnlock()

	if !exists {
		return nil, false
	}

	return m.GetSession(token)
}

// UpdateSessionEndpoint updates the UDP endpoint for a client in a session
func (m *UDPSessionManager) UpdateSessionEndpoint(token [UDPSessionTokenSize]byte, addr *net.UDPAddr, isClientA bool) error {
	m.sessionsLock.Lock()
	defer m.sessionsLock.Unlock()

	session, exists := m.sessions[token]
	if !exists {
		return ErrInvalidSessionToken
	}

	if isClientA {
		session.ClientAAddr = addr
		session.LastSeenA = time.Now()
	} else {
		session.ClientBAddr = addr
		session.LastSeenB = time.Now()
	}

	log.Debug().
		Str("lease_id", session.LeaseID).
		Str("session_token", SessionTokenToString(token)).
		Str("addr", addr.String()).
		Bool("is_client_a", isClientA).
		Msg("[UDPSessionManager] Session endpoint updated")

	return nil
}

// DeleteSession removes a session
func (m *UDPSessionManager) DeleteSession(token [UDPSessionTokenSize]byte) {
	m.sessionsLock.Lock()
	session, exists := m.sessions[token]
	if exists {
		delete(m.sessions, token)
	}
	m.sessionsLock.Unlock()

	if exists {
		m.leaseToSessionLock.Lock()
		delete(m.leaseToSession, session.LeaseID)
		m.leaseToSessionLock.Unlock()

		log.Debug().
			Str("lease_id", session.LeaseID).
			Str("session_token", SessionTokenToString(token)).
			Msg("[UDPSessionManager] Session deleted")
	}
}

// DeleteSessionByLease removes a session by lease ID
func (m *UDPSessionManager) DeleteSessionByLease(leaseID string) {
	m.leaseToSessionLock.Lock()
	token, exists := m.leaseToSession[leaseID]
	m.leaseToSessionLock.Unlock()

	if exists {
		m.DeleteSession(token)
	}
}

// GetActiveSessionCount returns the number of active sessions
func (m *UDPSessionManager) GetActiveSessionCount() int {
	m.sessionsLock.RLock()
	defer m.sessionsLock.RUnlock()
	return len(m.sessions)
}
