package portal

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/portal/acme"
	"github.com/gosuda/portal/v2/portal/discovery"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

func mustSignedRelayDescriptor(t *testing.T, ownerPrivateKey, relayURL string) types.RelayDescriptor {
	t.Helper()

	identity, err := utils.ResolveSecp256k1Identity(ownerPrivateKey)
	if err != nil {
		t.Fatalf("ResolveSecp256k1Identity() error = %v", err)
	}

	now := time.Now().UTC()
	wireGuardPrivateKey, err := utils.NormalizeWireGuardPrivateKey(strings.Repeat("44", 32))
	if err != nil {
		t.Fatalf("NormalizeWireGuardPrivateKey() error = %v", err)
	}
	wireGuardPublicKey, err := utils.WireGuardPublicKeyFromPrivate(wireGuardPrivateKey)
	if err != nil {
		t.Fatalf("WireGuardPublicKeyFromPrivate() error = %v", err)
	}
	overlayIPv4, err := utils.DeriveWireGuardOverlayIPv4(wireGuardPublicKey)
	if err != nil {
		t.Fatalf("DeriveWireGuardOverlayIPv4() error = %v", err)
	}
	desc, err := discovery.SignedDescriptor(types.RelayDescriptor{
		RelayID:             relayURL,
		OwnerAddress:        identity.Address,
		SignerPublicKey:     identity.PublicKey,
		Sequence:            uint64(now.UnixMilli()),
		Version:             1,
		IssuedAt:            now,
		ExpiresAt:           now.Add(time.Hour),
		APIHTTPSAddr:        relayURL,
		WireGuardPublicKey:  wireGuardPublicKey,
		WireGuardEndpoint:   net.JoinHostPort(utils.PortalRootHost(relayURL), "51820"),
		OverlayIPv4:         overlayIPv4,
		SupportsTCP:         true,
		SupportsOverlayPeer: true,
		StatusState:         "healthy",
	}, identity.PrivateKey)
	if err != nil {
		t.Fatalf("SignedDescriptor() error = %v", err)
	}
	return desc
}

func TestNewServerRequiresWireGuardWhenDiscoveryEnabled(t *testing.T) {
	t.Parallel()

	_, err := NewServer(ServerConfig{
		PortalURL:        "https://portal.example.com",
		DiscoveryEnabled: true,
	})
	if err == nil {
		t.Fatal("NewServer() error = nil, want wireguard requirement error")
	}
	if !strings.Contains(err.Error(), "wireguard private key is required when discovery is enabled") {
		t.Fatalf("NewServer() error = %v, want wireguard requirement error", err)
	}
}

func TestServerStartInitializesLocalACMEAndSigner(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:     "https://localhost:4017",
		ACME:          acme.Config{KeyDir: t.TempDir()},
		APIListenAddr: "127.0.0.1:0",
		SNIListenAddr: "127.0.0.1:0",
		UDPPortCount:  1,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := server.Start(ctx, nil); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		cancel()
		if err := server.Wait(); err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
	})

	healthResp, err := client.Get("https://" + utils.HostPortOrLoopback(server.APIAddr()) + types.PathHealthz)
	if err != nil {
		t.Fatalf("GET /healthz error = %v", err)
	}
	defer healthResp.Body.Close()

	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want %d", healthResp.StatusCode, http.StatusOK)
	}

	var healthEnvelope types.APIEnvelope[map[string]string]
	if err := json.NewDecoder(healthResp.Body).Decode(&healthEnvelope); err != nil {
		t.Fatalf("decode /healthz response: %v", err)
	}
	if !healthEnvelope.OK || healthEnvelope.Data["status"] != "ok" {
		t.Fatalf("GET /healthz response = %+v, want ok status", healthEnvelope)
	}

	signResp, err := client.Get("https://" + utils.HostPortOrLoopback(server.APIAddr()) + types.PathV1Sign)
	if err != nil {
		t.Fatalf("GET /v1/sign error = %v", err)
	}
	defer signResp.Body.Close()

	if signResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /v1/sign status = %d, want %d", signResp.StatusCode, http.StatusMethodNotAllowed)
	}
}

func TestServerStartRejectsMismatchedACMEBaseDomain(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:     "https://portal.example.com",
		ACME:          acme.Config{BaseDomain: "other.example.com", KeyDir: t.TempDir()},
		APIListenAddr: "127.0.0.1:0",
		SNIListenAddr: "127.0.0.1:0",
		UDPPortCount:  1,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	err = server.Start(context.Background(), nil)
	if err == nil {
		t.Fatal("Start() error = nil, want mismatch error")
	}
	if !strings.Contains(err.Error(), "does not match portal root host") {
		t.Fatalf("Start() error = %v, want base domain mismatch", err)
	}
}

func TestNewServerDerivesWireGuardConfigFromPrivateKey(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:           "https://portal.example.com",
		WireGuardPrivateKey: strings.Repeat("33", 32),
		DiscoveryPort:       41011,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	if server.wgConfig.PrivateKey == "" {
		t.Fatal("WireGuardPrivateKey = empty, want normalized key")
	}
	if server.wgConfig.PublicKey == "" {
		t.Fatal("WireGuardPublicKey = empty, want derived key")
	}
	if server.wgConfig.Endpoint != net.JoinHostPort("portal.example.com", "41011") {
		t.Fatalf("WireGuardEndpoint = %q, want %q", server.wgConfig.Endpoint, net.JoinHostPort("portal.example.com", "41011"))
	}
	if server.wgConfig.OverlayIPv4 == "" {
		t.Fatal("OverlayIPv4 = empty, want derived overlay address")
	}
	if err := utils.ValidateWireGuardEndpoint(server.wgConfig.Endpoint); err != nil {
		t.Fatalf("ValidateWireGuardEndpoint() error = %v", err)
	}
	if err := utils.ValidateOverlayIPv4(server.wgConfig.OverlayIPv4); err != nil {
		t.Fatalf("ValidateOverlayIPv4() error = %v", err)
	}

	wantOverlay, err := utils.DeriveWireGuardOverlayIPv4(server.wgConfig.PublicKey)
	if err != nil {
		t.Fatalf("DeriveWireGuardOverlayIPv4() error = %v", err)
	}
	if server.wgConfig.OverlayIPv4 != wantOverlay {
		t.Fatalf("OverlayIPv4 = %q, want %q", server.wgConfig.OverlayIPv4, wantOverlay)
	}
}

func TestNewServerIgnoresDiscoveryPortWithoutWireGuardKey(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:     "https://portal.example.com",
		DiscoveryPort: 51820,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if server.wgConfig.Endpoint != "" {
		t.Fatalf("WireGuardEndpoint = %q, want empty without wireguard key", server.wgConfig.Endpoint)
	}
}

func TestRegisterLeaseDerivesFixedHostnameFromName(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:    "https://portal.example.com",
		UDPPortCount: 1,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	resp, err := server.registerLease(types.RegisterRequest{
		Name:         "Demo-App",
		ReverseToken: "tok_1",
	}, "203.0.113.10")
	if err != nil {
		t.Fatalf("registerLease() error = %v", err)
	}

	wantHostname := "demo-app.portal.example.com"
	if resp.Hostname != wantHostname {
		t.Fatalf("registerLease() hostname = %q, want %q", resp.Hostname, wantHostname)
	}

	record, ok := server.registry.Get(resp.LeaseID)
	if !ok {
		t.Fatal("registry.Get() = false, want registered lease")
	}
	snapshot := server.registry.Snapshot(record)
	if snapshot.Name != "demo-app" {
		t.Fatalf("Snapshot().Name = %q, want %q", snapshot.Name, "demo-app")
	}
	if snapshot.Hostname != wantHostname {
		t.Fatalf("Snapshot().Hostname = %q, want %q", snapshot.Hostname, wantHostname)
	}
}

func TestRegisterLeaseBuildsUDPEnabledRuntime(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:    "https://portal.example.com",
		UDPPortCount: 10,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	server.registry.policy.SetUDPPolicy(true, 0)

	resp, err := server.registerLease(types.RegisterRequest{
		Name:         "demo-udp",
		ReverseToken: "tok_udp",
		UDPEnabled:   true,
	}, "203.0.113.10")
	if err != nil {
		t.Fatalf("registerLease() error = %v", err)
	}
	t.Cleanup(func() {
		if record, ok := server.registry.Get(resp.LeaseID); ok {
			record.Close()
		}
	})

	record, ok := server.registry.Get(resp.LeaseID)
	if !ok {
		t.Fatal("registry.Get() = false, want registered lease")
	}
	if record.stream == nil {
		t.Fatal("stream = nil, want stream runtime")
	}
	if record.datagram == nil {
		t.Fatal("datagram = nil, want datagram runtime")
	}
	if got := record.datagram.UDPPort(); got == 0 {
		t.Fatal("UDPPort() = 0, want allocated port")
	}
	if resp.UDPAddr == "" {
		t.Fatal("RegisterResponse.UDPAddr = empty, want public udp address")
	}
}

func TestServerUpsertDiscoverySeedURLsSkipsLocalRelayHosts(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:           "https://portal.example.com",
		Bootstraps:          []string{"https://bootstrap.example.com"},
		WireGuardPrivateKey: strings.Repeat("23", 32),
		DiscoveryPort:       41022,
		DiscoveryEnabled:    true,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	added, err := server.peerRegistry.registerBootstrapURLs([]string{
		"https://localhost:4017",
		"https://relay-a.example.com",
		"https://127.0.0.1:4017",
	})
	if err != nil {
		t.Fatalf("UpsertSeedURLs() error = %v", err)
	}

	if !reflect.DeepEqual(added, []string{"https://relay-a.example.com"}) {
		t.Fatalf("UpsertSeedURLs() added = %v, want [%q]", added, "https://relay-a.example.com")
	}
	knownRelayURLs, err := utils.ExcludeLocalRelayURLs("https://bootstrap.example.com", "https://relay-a.example.com")
	if err != nil {
		t.Fatalf("ExcludeLocalRelayURLs() error = %v", err)
	}
	knownURLs := make([]string, 0, len(server.peerRegistry.bootstrapPeers()))
	for _, descriptor := range server.peerRegistry.bootstrapPeers() {
		if apiURL := strings.TrimSpace(descriptor.APIHTTPSAddr); apiURL != "" {
			knownURLs = append(knownURLs, apiURL)
		}
	}
	knownURLs, err = utils.ExcludeLocalRelayURLs(knownURLs...)
	if err != nil {
		t.Fatalf("ExcludeLocalRelayURLs() known error = %v", err)
	}
	if !reflect.DeepEqual(knownURLs, knownRelayURLs) {
		t.Fatalf("BootstrapDescriptors() = %v, want [%q %q]", knownURLs, "https://bootstrap.example.com", "https://relay-a.example.com")
	}
	if len(server.peerRegistry.syncablePeers()) != 0 {
		t.Fatalf("syncablePeers() = %v, want empty before direct confirmation", server.peerRegistry.syncablePeers())
	}
	if len(server.peerRegistry.advertisedPeers()) != 0 {
		t.Fatalf("advertisedPeers() = %v, want empty before direct confirmation", server.peerRegistry.advertisedPeers())
	}
}

func TestServerRecordVerifiedDiscoveryPeerRequiresDirectConfirmation(t *testing.T) {
	t.Parallel()

	ownerPrivateKey := strings.Repeat("11", 32)
	server, err := NewServer(ServerConfig{
		PortalURL:           "https://portal.example.com",
		Bootstraps:          []string{"https://bootstrap.example.com"},
		OwnerPrivateKey:     ownerPrivateKey,
		WireGuardPrivateKey: strings.Repeat("24", 32),
		DiscoveryPort:       41023,
		DiscoveryEnabled:    true,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	bootstrapDesc := mustSignedRelayDescriptor(t, ownerPrivateKey, "https://bootstrap.example.com")
	relayADesc := mustSignedRelayDescriptor(t, ownerPrivateKey, "https://relay-a.example.com")

	added, changed, err := server.peerRegistry.register(bootstrapDesc, true)
	if err != nil {
		t.Fatalf("RecordVerified() error = %v", err)
	}
	if added {
		t.Fatal("RecordVerified() added = true, want false for seeded bootstrap")
	}
	if !changed {
		t.Fatal("RecordVerified() changed = false, want true")
	}

	added, changed, err = server.peerRegistry.register(relayADesc, false)
	if err != nil {
		t.Fatalf("RecordVerified() hinted error = %v", err)
	}
	if !added {
		t.Fatal("RecordVerified() hinted add = false, want true")
	}
	if !changed {
		t.Fatal("RecordVerified() hinted changed = false, want true")
	}
	knownURLs := make([]string, 0, len(server.peerRegistry.bootstrapPeers()))
	for _, descriptor := range server.peerRegistry.bootstrapPeers() {
		if apiURL := strings.TrimSpace(descriptor.APIHTTPSAddr); apiURL != "" {
			knownURLs = append(knownURLs, apiURL)
		}
	}
	knownURLs, err = utils.ExcludeLocalRelayURLs(knownURLs...)
	if err != nil {
		t.Fatalf("ExcludeLocalRelayURLs() known error = %v", err)
	}
	if !reflect.DeepEqual(knownURLs, []string{"https://bootstrap.example.com"}) {
		t.Fatalf("BootstrapDescriptors() = %v, want [%q]", knownURLs, "https://bootstrap.example.com")
	}
	syncableURLs := make([]string, 0, len(server.peerRegistry.syncablePeers()))
	for _, descriptor := range server.peerRegistry.syncablePeers() {
		if apiURL := strings.TrimSpace(descriptor.APIHTTPSAddr); apiURL != "" {
			syncableURLs = append(syncableURLs, apiURL)
		}
	}
	syncableURLs, err = utils.ExcludeLocalRelayURLs(syncableURLs...)
	if err != nil {
		t.Fatalf("ExcludeLocalRelayURLs() syncable error = %v", err)
	}
	if !reflect.DeepEqual(syncableURLs, []string{"https://relay-a.example.com"}) {
		t.Fatalf("SyncablePeerDescriptors() = %v, want [%q]", syncableURLs, "https://relay-a.example.com")
	}
	advertisedURLs := make([]string, 0, len(server.peerRegistry.advertisedPeers()))
	for _, descriptor := range server.peerRegistry.advertisedPeers() {
		if apiURL := strings.TrimSpace(descriptor.APIHTTPSAddr); apiURL != "" {
			advertisedURLs = append(advertisedURLs, apiURL)
		}
	}
	advertisedURLs, err = utils.ExcludeLocalRelayURLs(advertisedURLs...)
	if err != nil {
		t.Fatalf("ExcludeLocalRelayURLs() advertised error = %v", err)
	}
	if !reflect.DeepEqual(advertisedURLs, []string{"https://bootstrap.example.com"}) {
		t.Fatalf("AdvertisedDescriptors() = %v, want [%q]", advertisedURLs, "https://bootstrap.example.com")
	}

	snapshot := server.peerRegistry.snapshot()
	if snapshot[bootstrapDesc.RelayID].State != types.PeerStateAdvertised {
		t.Fatalf("bootstrap state = %q, want %q", snapshot[bootstrapDesc.RelayID].State, types.PeerStateAdvertised)
	}
	if snapshot[relayADesc.RelayID].State != types.PeerStateVerified {
		t.Fatalf("relay-a state = %q, want %q", snapshot[relayADesc.RelayID].State, types.PeerStateVerified)
	}

	added, changed, err = server.peerRegistry.register(relayADesc, true)
	if err != nil {
		t.Fatalf("RecordVerified() second error = %v", err)
	}
	if added {
		t.Fatal("RecordVerified() second add = true, want false")
	}
	if !changed {
		t.Fatal("RecordVerified() second changed = false, want true")
	}
	advertisedURLs = advertisedURLs[:0]
	for _, descriptor := range server.peerRegistry.advertisedPeers() {
		if apiURL := strings.TrimSpace(descriptor.APIHTTPSAddr); apiURL != "" {
			advertisedURLs = append(advertisedURLs, apiURL)
		}
	}
	advertisedURLs, err = utils.ExcludeLocalRelayURLs(advertisedURLs...)
	if err != nil {
		t.Fatalf("ExcludeLocalRelayURLs() advertised second error = %v", err)
	}
	if !reflect.DeepEqual(advertisedURLs, []string{"https://bootstrap.example.com", "https://relay-a.example.com"}) {
		t.Fatalf("AdvertisedDescriptors() = %v, want [%q %q]", advertisedURLs, "https://bootstrap.example.com", "https://relay-a.example.com")
	}
}

func TestServerStartHidesDiscoveryRoutesWhenDisabled(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:     "https://localhost:4017",
		ACME:          acme.Config{KeyDir: t.TempDir()},
		APIListenAddr: "127.0.0.1:0",
		SNIListenAddr: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := server.Start(ctx, nil); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		cancel()
		if err := server.Wait(); err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
	})

	resp, err := client.Get("https://" + utils.HostPortOrLoopback(server.APIAddr()) + types.PathDiscovery + "?root_host=localhost&name=demo")
	if err != nil {
		t.Fatalf("GET discovery resolve error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET discovery resolve status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	if server.DiscoveryEnabled() {
		t.Fatal("DiscoveryEnabled() = true, want false without configured discovery service")
	}
}
