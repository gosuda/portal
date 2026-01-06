package portal

import (
	"sync"
	"time"

	"gosuda.org/portal/portal/corev2/common"
)

// SessionManagerV2 manages V2 sessions for relay server
type SessionManagerV2 struct {
	sessions     map[common.SessionID]*RelaySessionV2
	sessionsLock sync.RWMutex

	// Cleanup
	stopCh      chan struct{}
	ttlInterval time.Duration
}

// NewSessionManagerV2 creates a new V2 session manager
func NewSessionManagerV2(ttlInterval time.Duration) *SessionManagerV2 {
	sm := &SessionManagerV2{
		sessions:    make(map[common.SessionID]*RelaySessionV2),
		stopCh:      make(chan struct{}),
		ttlInterval: ttlInterval,
	}
	go sm.cleanupWorker()
	return sm
}

// AddSession adds a new session
func (sm *SessionManagerV2) AddSession(session *RelaySessionV2) {
	sm.sessionsLock.Lock()
	defer sm.sessionsLock.Unlock()
	sm.sessions[session.SessionID] = session
}

// GetSession retrieves a session by ID
func (sm *SessionManagerV2) GetSession(sessionID common.SessionID) *RelaySessionV2 {
	sm.sessionsLock.RLock()
	defer sm.sessionsLock.RUnlock()
	return sm.sessions[sessionID]
}

// RemoveSession removes and closes a session
func (sm *SessionManagerV2) RemoveSession(sessionID common.SessionID) {
	sm.sessionsLock.Lock()
	defer sm.sessionsLock.Unlock()

	if session, exists := sm.sessions[sessionID]; exists {
		session.Close()
		delete(sm.sessions, sessionID)
	}
}

// GetAllSessions returns all active sessions
func (sm *SessionManagerV2) GetAllSessions() []*RelaySessionV2 {
	sm.sessionsLock.RLock()
	defer sm.sessionsLock.RUnlock()

	sessions := make([]*RelaySessionV2, 0, len(sm.sessions))
	for _, sess := range sm.sessions {
		if !sess.IsClosed() {
			sessions = append(sessions, sess)
		}
	}
	return sessions
}

// GetSessionCount returns number of active sessions
func (sm *SessionManagerV2) GetSessionCount() int {
	sm.sessionsLock.RLock()
	defer sm.sessionsLock.RUnlock()
	return len(sm.sessions)
}

// UpdateSessionLastSeen updates the last seen time for a session
func (sm *SessionManagerV2) UpdateSessionLastSeen(sessionID common.SessionID) {
	sm.sessionsLock.Lock()
	defer sm.sessionsLock.Unlock()

	if session, exists := sm.sessions[sessionID]; exists {
		session.mu.Lock()
		session.LastSeen = time.Now()
		session.mu.Unlock()
	}
}

// cleanupWorker periodically removes expired sessions
func (sm *SessionManagerV2) cleanupWorker() {
	ticker := time.NewTicker(sm.ttlInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sm.cleanupExpiredSessions()
		case <-sm.stopCh:
			return
		}
	}
}

// cleanupExpiredSessions removes expired sessions
func (sm *SessionManagerV2) cleanupExpiredSessions() {
	sm.sessionsLock.Lock()
	defer sm.sessionsLock.Unlock()

	now := time.Now()
	for sessionID, session := range sm.sessions {
		if now.After(session.Expires) || session.IsClosed() {
			session.Close()
			delete(sm.sessions, sessionID)
		}
	}
}

// Stop stops the session manager
func (sm *SessionManagerV2) Stop() {
	close(sm.stopCh)

	// Close all sessions
	sm.sessionsLock.Lock()
	for _, session := range sm.sessions {
		session.Close()
	}
	sm.sessions = make(map[common.SessionID]*RelaySessionV2)
	sm.sessionsLock.Unlock()
}

// GetSessionStats returns statistics for all sessions
func (sm *SessionManagerV2) GetSessionStats() map[string]uint64 {
	sm.sessionsLock.RLock()
	defer sm.sessionsLock.RUnlock()

	totalSessions := uint64(len(sm.sessions))
	totalBytesSent := uint64(0)
	totalBytesRecv := uint64(0)
	totalPacketsSent := uint64(0)
	totalPacketsRecv := uint64(0)

	for _, session := range sm.sessions {
		session.mu.RLock()
		totalBytesSent += session.BytesSent
		totalBytesRecv += session.BytesReceived
		totalPacketsSent += session.PacketsSent
		totalPacketsRecv += session.PacketsRecv
		session.mu.RUnlock()
	}

	return map[string]uint64{
		"total_sessions":     totalSessions,
		"total_bytes_sent":   totalBytesSent,
		"total_bytes_recv":   totalBytesRecv,
		"total_packets_sent": totalPacketsSent,
		"total_packets_recv": totalPacketsRecv,
	}
}
