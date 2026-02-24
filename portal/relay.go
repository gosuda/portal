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

// GetLeaseByName returns a lease entry by its name.
func (g *RelayServer) GetLeaseByName(name string) (*LeaseEntry, bool) {
	return g.leaseManager.GetLeaseByName(name)
}

// GetLeaseByNameFold returns a lease entry by name using case-insensitive matching.
func (g *RelayServer) GetLeaseByNameFold(name string) (*LeaseEntry, bool) {
	return g.leaseManager.GetLeaseByNameFold(name)
}

// GetAllLeaseEntries returns all lease entries from the lease manager.
func (g *RelayServer) GetAllLeaseEntries() []*LeaseEntry {
	return g.leaseManager.GetAllLeaseEntries()
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
