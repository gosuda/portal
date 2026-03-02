package portal

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/portal/acme"
	"gosuda.org/portal/portal/keyless"
	"gosuda.org/portal/portal/sni"
)

type RelayServer struct {
	address  []string
	BaseHost string

	leaseManager  *LeaseManager
	reverseHub    *ReverseHub
	sniRouter     *sni.Router
	acmeManager   *acme.Manager
	keylessSigner *keyless.Signer

	stopch    chan struct{}
	waitgroup sync.WaitGroup
}

// NewRelayServer creates a new relay server.
func NewRelayServer(
	ctx context.Context,
	address []string,
	sniPort string,
	baseHost string,
	keylessKey string,
	cloudflareToken string,
) (*RelayServer, error) {
	server := &RelayServer{
		BaseHost:     strings.ToLower(strings.TrimSpace(baseHost)),
		address:      address,
		leaseManager: NewLeaseManager(30 * time.Second),
		reverseHub:   NewReverseHub(),
		sniRouter:    sni.NewRouter(sniPort),
		acmeManager: acme.NewManager(acme.Config{
			BaseDomain:      strings.ToLower(strings.TrimSpace(baseHost)),
			KeyFile:         keylessKey,
			CloudflareToken: cloudflareToken,
		}),
		stopch: make(chan struct{}),
	}

	keyFile, err := server.acmeManager.EnsureSigningKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("ensure keyless signing key: %w", err)
	}

	signer, err := keyless.NewSigner(keyless.Config{
		KeyFile: keyFile,
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

// GetACMEManager returns relay ACME manager.
func (g *RelayServer) GetACMEManager() *acme.Manager {
	return g.acmeManager
}

// ConfigurePortalRootFallback forwards unmatched root-domain SNI traffic to the provided upstream listener.
func (g *RelayServer) ConfigurePortalRootFallback(rootSNI, upstreamAddr string) {
	if g == nil || g.sniRouter == nil {
		return
	}

	rootSNI = strings.ToLower(strings.TrimSpace(rootSNI))
	if rootSNI == "" {
		return
	}

	upstreamAddr = strings.TrimSpace(upstreamAddr)
	if upstreamAddr == "" {
		log.Warn().
			Msg("[RelayServer] root-domain SNI fallback upstream is empty; fallback disabled")
		return
	}

	g.sniRouter.SetNoRouteHandler(func(clientConn net.Conn, serverName string) bool {
		if !strings.EqualFold(strings.TrimSpace(serverName), rootSNI) {
			return false
		}

		upstreamConn, err := net.DialTimeout("tcp", upstreamAddr, 5*time.Second)
		if err != nil {
			log.Warn().
				Err(err).
				Str("sni", serverName).
				Str("upstream", upstreamAddr).
				Msg("[SNI] failed to forward root domain to admin/API listener")
			clientConn.Close()
			return true
		}

		log.Debug().
			Str("sni", serverName).
			Str("upstream", upstreamAddr).
			Msg("[SNI] forwarding root domain to admin/API listener")
		sni.BridgeConnections(clientConn, upstreamConn)
		return true
	})
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
