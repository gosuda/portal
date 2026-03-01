package portal

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal/keyless"
	"gosuda.org/portal/portal/sni"
)

type RelayServer struct {
	address  []string
	BaseHost string

	leaseManager  *LeaseManager
	reverseHub    *ReverseHub
	sniRouter     *sni.Router
	keylessSigner *keyless.Signer

	stopch    chan struct{}
	waitgroup sync.WaitGroup
}

// NewRelayServer creates a new relay server.
func NewRelayServer(
	ctx context.Context,
	address []string,
	sniPort string,
	portalURL string,
	keylessKey string,
) (*RelayServer, error) {
	baseDomain := extractBaseDomain(portalURL)
	if baseDomain == "" {
		log.Warn().Msg("[RelayServer] Could not extract base domain from portal URL")
	}

	server := &RelayServer{
		BaseHost:     baseDomain,
		address:      address,
		leaseManager: NewLeaseManager(30 * time.Second),
		reverseHub:   NewReverseHub(),
		sniRouter:    sni.NewRouter(sniPort),
		stopch:       make(chan struct{}),
	}

	signer, err := keyless.NewSigner(keyless.Config{
		KeyFile: keylessKey,
	})
	if err != nil {
		return nil, fmt.Errorf("configure keyless signer: %w", err)
	}
	server.keylessSigner = signer
	if signer != nil {
		log.Info().
			Str("key_id", signer.KeyID()).
			Msg("[signer] keyless signer enabled at /v1/sign")
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
	return server, nil
}

func extractBaseDomain(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}

	u, err := url.Parse(trimmed)
	if err != nil || u.Hostname() == "" {
		return ""
	}

	host := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(u.Hostname())), "*.")
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}

// GetLeaseManager returns the lease manager instance.
func (g *RelayServer) GetLeaseManager() *LeaseManager {
	return g.leaseManager
}

// GetReverseHub returns the reverse hub instance.
func (g *RelayServer) GetReverseHub() *ReverseHub {
	return g.reverseHub
}

// GetSNIRouter returns the SNI router instance.
func (g *RelayServer) GetSNIRouter() *sni.Router {
	return g.sniRouter
}

// GetKeylessSigner returns relay keyless signer when configured.
func (g *RelayServer) GetKeylessSigner() *keyless.Signer {
	return g.keylessSigner
}

// Start starts the relay server.
func (g *RelayServer) Start() error {
	g.leaseManager.Start()

	if err := g.sniRouter.Start(); err != nil {
		log.Error().Err(err).Str("addr", g.sniRouter.GetAddr()).Msg("[RelayServer] Failed to start SNI router")
		return err
	}
	log.Info().Str("addr", g.sniRouter.GetAddr()).Msg("[RelayServer] SNI router started")

	log.Info().Msg("[RelayServer] Started")
	return nil
}

// Stop stops the relay server.
func (g *RelayServer) Stop() {
	close(g.stopch)
	g.leaseManager.Stop()
	g.sniRouter.Stop()
	g.waitgroup.Wait()
	log.Info().Msg("[RelayServer] Stopped")
}
