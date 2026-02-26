package portal

import (
	"crypto/subtle"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

type RelayServer struct {
	address []string

	leaseManager *LeaseManager
	reverseHub   *ReverseHub

	stopch    chan struct{}
	waitgroup sync.WaitGroup
}

// NewRelayServer creates a new relay server.
func NewRelayServer(address []string) *RelayServer {
	server := &RelayServer{
		address:      address,
		leaseManager: NewLeaseManager(30 * time.Second),
		reverseHub:   NewReverseHub(),
		stopch:       make(chan struct{}),
	}
	server.leaseManager.SetOnLeaseDeleted(server.reverseHub.DropLease)
	server.reverseHub.SetAuthorizer(func(leaseID, token string) bool {
		entry, ok := server.leaseManager.GetLeaseByID(strings.TrimSpace(leaseID))
		if !ok || entry == nil || entry.Lease == nil {
			return false
		}
		expected := strings.TrimSpace(entry.Lease.ReverseToken)
		if expected == "" {
			return false
		}
		return subtle.ConstantTimeCompare([]byte(expected), []byte(strings.TrimSpace(token))) == 1
	})
	return server
}

// GetLeaseManager returns the lease manager instance.
func (g *RelayServer) GetLeaseManager() *LeaseManager {
	return g.leaseManager
}

// GetReverseHub returns the reverse hub instance.
func (g *RelayServer) GetReverseHub() *ReverseHub {
	return g.reverseHub
}

// Start starts the relay server.
func (g *RelayServer) Start() {
	g.leaseManager.Start()
	log.Info().Msg("[RelayServer] Started")
}

// Stop stops the relay server.
func (g *RelayServer) Stop() {
	close(g.stopch)
	g.leaseManager.Stop()
	g.waitgroup.Wait()
	log.Info().Msg("[RelayServer] Stopped")
}
