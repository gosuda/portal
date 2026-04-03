package wireguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

type Config struct {
	PrivateKey   string
	PublicKey    string
	Endpoint     string
	OverlayIPv4  string
	OverlayCIDRs []string
	ListenPort   int
}

func NormalizeConfig(rootHost string, cfg Config) (Config, error) {
	configured := strings.TrimSpace(cfg.PrivateKey) != "" ||
		strings.TrimSpace(cfg.PublicKey) != "" ||
		strings.TrimSpace(cfg.Endpoint) != "" ||
		strings.TrimSpace(cfg.OverlayIPv4) != "" ||
		len(cfg.OverlayCIDRs) > 0
	if !configured {
		return cfg, nil
	}

	if strings.TrimSpace(cfg.PrivateKey) == "" {
		return Config{}, errors.New("wireguard private key is required when relay overlay is enabled")
	}

	privateKey, err := utils.NormalizeWireGuardPrivateKey(cfg.PrivateKey)
	if err != nil {
		return Config{}, fmt.Errorf("normalize wireguard private key: %w", err)
	}
	publicKey, err := utils.WireGuardPublicKeyFromPrivate(privateKey)
	if err != nil {
		return Config{}, fmt.Errorf("derive wireguard public key: %w", err)
	}
	if configuredPublicKey := strings.TrimSpace(cfg.PublicKey); configuredPublicKey != "" && configuredPublicKey != publicKey {
		return Config{}, errors.New("wireguard public key does not match private key")
	}

	cfg.PrivateKey = privateKey
	cfg.PublicKey = publicKey
	cfg.ListenPort = utils.IntOrDefault(cfg.ListenPort, DefaultListenPort)
	if len(cfg.OverlayCIDRs) > 0 {
		cfg.OverlayCIDRs, err = utils.NormalizeOverlayCIDRs(cfg.OverlayCIDRs)
		if err != nil {
			return Config{}, fmt.Errorf("normalize overlay cidrs: %w", err)
		}
	}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		cfg.Endpoint = net.JoinHostPort(rootHost, fmt.Sprintf("%d", cfg.ListenPort))
	}
	if strings.TrimSpace(cfg.OverlayIPv4) == "" {
		cfg.OverlayIPv4, err = utils.DeriveWireGuardOverlayIPv4(cfg.PublicKey)
		if err != nil {
			return Config{}, fmt.Errorf("derive overlay ipv4: %w", err)
		}
	}
	if err := utils.ValidateWireGuardEndpoint(cfg.Endpoint); err != nil {
		return Config{}, err
	}
	if err := utils.ValidateOverlayIPv4(cfg.OverlayIPv4); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

type Overlay struct {
	stack    *stack
	listener net.Listener
	server   *http.Server
}

func NewOverlay(cfg Config, handler http.Handler) (*Overlay, error) {
	stack, err := newStack(cfg)
	if err != nil {
		return nil, err
	}

	listener, err := stack.ListenTCP(DefaultPeerAPIHTTPPort)
	if err != nil {
		_ = stack.Close()
		return nil, err
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	return &Overlay{
		stack:    stack,
		listener: listener,
		server:   server,
	}, nil
}

func (o *Overlay) Serve() error {
	if o == nil || o.server == nil || o.listener == nil {
		return nil
	}

	err := o.server.Serve(o.listener)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (o *Overlay) Shutdown(ctx context.Context) error {
	if o == nil {
		return nil
	}

	var shutdownErr error
	if o.server != nil {
		err := o.server.Shutdown(ctx)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}
	if o.listener != nil {
		err := o.listener.Close()
		if err != nil && !errors.Is(err, net.ErrClosed) {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}
	if o.stack != nil {
		shutdownErr = errors.Join(shutdownErr, o.stack.Close())
	}
	return shutdownErr
}

func (o *Overlay) Client() *http.Client {
	if o == nil || o.stack == nil {
		return nil
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:       o.stack.DialContext,
			ForceAttemptHTTP2: false,
		},
	}
}

func (o *Overlay) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if o == nil || o.stack == nil {
		return nil, errors.New("overlay is not initialized")
	}
	return o.stack.DialContext(ctx, network, address)
}

func (o *Overlay) ListenTCP(port int) (net.Listener, error) {
	if o == nil || o.stack == nil {
		return nil, errors.New("overlay is not initialized")
	}
	return o.stack.ListenTCP(port)
}

func (o *Overlay) Sync(selfIdentityKey string, snapshot map[string]types.RelayState) error {
	if o == nil || o.stack == nil {
		return nil
	}
	return o.stack.ApplyPeers(peersForSnapshot(selfIdentityKey, snapshot))
}

func peersForSnapshot(selfIdentityKey string, snapshot map[string]types.RelayState) []types.DesiredPeer {
	peers := make([]types.DesiredPeer, 0, len(snapshot))
	for _, state := range snapshot {
		if state.Expired {
			continue
		}
		desc := state.Descriptor
		if desc.Key() == selfIdentityKey || !desc.SupportsOverlayPeer {
			continue
		}
		if desc.WireGuardPublicKey == "" || desc.WireGuardEndpoint == "" || desc.OverlayIPv4 == "" {
			continue
		}

		allowedIPs := []string{desc.OverlayIPv4 + "/32"}
		allowedIPs = append(allowedIPs, desc.OverlayCIDRs...)
		peers = append(peers, types.DesiredPeer{
			WireGuardPublicKey: desc.WireGuardPublicKey,
			WireGuardEndpoint:  desc.WireGuardEndpoint,
			AllowedIPs:         allowedIPs,
		})
	}
	sort.Slice(peers, func(i, j int) bool {
		return peers[i].WireGuardPublicKey < peers[j].WireGuardPublicKey
	})
	return peers
}
