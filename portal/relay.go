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
	acmeManager   *acme.AcmeManager
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
	keylessDir string,
	cloudflareToken string,
) (*RelayServer, error) {
	server := &RelayServer{
		BaseHost:     baseHost,
		address:      address,
		leaseManager: NewLeaseManager(30 * time.Second),
		reverseHub:   NewReverseHub(),
		sniRouter:    sni.NewRouter(sniPort),
		stopch:       make(chan struct{}),
	}

	keyFile := ""
	if keylessDir != "" {
		server.acmeManager = acme.NewManager(acme.Config{
			BaseDomain:      baseHost,
			KeyDir:          keylessDir,
			CloudflareToken: cloudflareToken,
		})
		keyFile = server.acmeManager.SigningKeyFile()
	}

	shouldEnsureWithACME := keylessDir != "" && cloudflareToken != "" && baseHost != ""
	if shouldEnsureWithACME {
		var err error
		keyFile, err = server.acmeManager.EnsureSigningKey(ctx)
		if err != nil {
			return nil, fmt.Errorf("ensure keyless signing key: %w", err)
		}
	} else {
		log.Info().
			Bool("has_key_dir", keylessDir != "").
			Bool("has_cloudflare_token", cloudflareToken != "").
			Bool("has_base_domain", baseHost != "").
			Msg("[signer] ACME issuance disabled (requires key directory, Cloudflare token, and base domain)")
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
		entry, ok := server.leaseManager.GetLeaseByID(leaseID)
		if !ok || entry == nil || entry.Lease == nil {
			return false
		}
		expected := entry.Lease.ReverseToken
		if expected == "" {
			return false
		}
		return subtle.ConstantTimeCompare([]byte(expected), []byte(token)) == 1
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
func (g *RelayServer) GetACMEManager() *acme.AcmeManager {
	return g.acmeManager
}

// ConfigurePortalRootFallback forwards unmatched root-domain SNI traffic to the provided upstream listener.
func (g *RelayServer) ConfigurePortalRootFallback(rootSNI, upstreamAddr string) {
	if g == nil || g.sniRouter == nil {
		return
	}

	if rootSNI == "" {
		return
	}

	if upstreamAddr == "" {
		log.Warn().
			Msg("[RelayServer] root-domain SNI fallback upstream is empty; fallback disabled")
		return
	}

	g.sniRouter.SetNoRouteHandler(func(clientConn net.Conn, serverName string) bool {
		if !strings.EqualFold(serverName, rootSNI) {
			return false
		}

		dialer := &net.Dialer{Timeout: 5 * time.Second}
		upstreamConn, err := dialer.DialContext(context.Background(), "tcp", upstreamAddr)
		if err != nil {
			log.Warn().
				Err(err).
				Str("sni", serverName).
				Str("upstream", upstreamAddr).
				Msg("[SNI] failed to forward root domain to admin/API listener")
			if closeErr := clientConn.Close(); closeErr != nil {
				log.Debug().Err(closeErr).Str("sni", serverName).Msg("[SNI] failed to close client connection")
			}
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

	// Start ACME renewal loop
	if g.acmeManager != nil {
		g.acmeManager.Start(context.Background())
	}

	log.Info().Msg("[RelayServer] Started")
	return nil
}

// Stop stops the relay server.
func (g *RelayServer) Stop() {
	close(g.stopch)
	g.leaseManager.Stop()
	if err := g.sniRouter.Stop(); err != nil {
		log.Warn().Err(err).Msg("[RelayServer] Failed to stop SNI router")
	}
	if g.acmeManager != nil {
		g.acmeManager.Stop()
	}
	g.waitgroup.Wait()
	log.Info().Msg("[RelayServer] Stopped")
}
