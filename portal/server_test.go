package portal

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/portal/acme"
	"github.com/gosuda/portal/v2/portal/discovery"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

func mustRelayDescriptor(t *testing.T, relayURL string) types.RelayDescriptor {
	t.Helper()

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
	desc, err := discovery.NormalizeDescriptor(types.RelayDescriptor{
		RelayID:             relayURL,
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
	})
	if err != nil {
		t.Fatalf("NormalizeDescriptor() error = %v", err)
	}
	return desc
}

func TestNewServerGeneratesWireGuardWhenDiscoveryEnabled(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:        "https://portal.example.com",
		DiscoveryEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if server.wgConfig.PrivateKey == "" {
		t.Fatal("WireGuardPrivateKey = empty, want generated key")
	}
	if server.wgConfig.PublicKey == "" {
		t.Fatal("WireGuardPublicKey = empty, want derived key")
	}
	if server.wgConfig.Endpoint == "" {
		t.Fatal("WireGuardEndpoint = empty, want derived endpoint")
	}
	if server.wgConfig.OverlayIPv4 == "" {
		t.Fatal("OverlayIPv4 = empty, want derived overlay address")
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

func TestServerStartDiscoveryOmitsOwnerIdentityFields(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:        "https://localhost:4017",
		ACME:             acme.Config{KeyDir: t.TempDir()},
		APIListenAddr:    "127.0.0.1:0",
		SNIListenAddr:    "127.0.0.1:0",
		DiscoveryEnabled: true,
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

	resp, err := client.Get("https://" + utils.HostPortOrLoopback(server.APIAddr()) + types.PathDiscovery)
	if err != nil {
		t.Fatalf("GET /discovery error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /discovery status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /discovery response: %v", err)
	}
	for _, key := range []string{"owner_address", "signer_public_key", "descriptor_signature"} {
		if strings.Contains(string(body), key) {
			t.Fatalf("/discovery body = %q, want %q omitted", string(body), key)
		}
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

	resp, err := server.registerLease(types.RegisterChallengeRequest{
		Name:         "Demo-App",
		OwnerAddress: server.OwnerAddress(),
	}, "203.0.113.10", "")
	if err != nil {
		t.Fatalf("registerLease() error = %v", err)
	}

	wantHostname := "demo-app.portal.example.com"
	if resp.Hostname != wantHostname {
		t.Fatalf("registerLease() hostname = %q, want %q", resp.Hostname, wantHostname)
	}

	record, err := server.registry.FindByID(resp.LeaseID)
	if err != nil {
		t.Fatalf("registry.FindByID() error = %v, want registered lease", err)
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

	resp, err := server.registerLease(types.RegisterChallengeRequest{
		Name:         "demo-udp",
		OwnerAddress: server.OwnerAddress(),
		UDPEnabled:   true,
	}, "203.0.113.10", "")
	if err != nil {
		t.Fatalf("registerLease() error = %v", err)
	}
	t.Cleanup(func() {
		if record, err := server.registry.FindByID(resp.LeaseID); err == nil {
			record.Close()
		}
	})

	record, err := server.registry.FindByID(resp.LeaseID)
	if err != nil {
		t.Fatalf("registry.FindByID() error = %v, want registered lease", err)
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

	added, err := server.relaySet.RegisterBootstrapRelayURLs([]string{
		"https://localhost:4017",
		"https://relay-a.example.com",
		"https://127.0.0.1:4017",
	}, time.Now().UTC())
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
	bootstrapDescriptors := server.relaySet.BootstrapDescriptors()
	syncableDescriptors := server.relaySet.SyncableDescriptors()
	advertisedDescriptors := server.relaySet.AdvertisedDescriptors()
	knownURLs := make([]string, 0, len(bootstrapDescriptors))
	for _, descriptor := range bootstrapDescriptors {
		if strings.TrimSpace(descriptor.APIHTTPSAddr) == "" {
			continue
		}
		knownURLs = append(knownURLs, descriptor.APIHTTPSAddr)
	}
	sort.Strings(knownURLs)
	knownURLs, err = utils.ExcludeLocalRelayURLs(knownURLs...)
	if err != nil {
		t.Fatalf("ExcludeLocalRelayURLs() known error = %v", err)
	}
	if !reflect.DeepEqual(knownURLs, knownRelayURLs) {
		t.Fatalf("BootstrapDescriptors() = %v, want [%q %q]", knownURLs, "https://bootstrap.example.com", "https://relay-a.example.com")
	}
	if len(syncableDescriptors) != 0 {
		t.Fatalf("syncable count = %d, want 0 before direct confirmation", len(syncableDescriptors))
	}
	if len(advertisedDescriptors) != 0 {
		t.Fatalf("advertised count = %d, want 0 before direct confirmation", len(advertisedDescriptors))
	}
}

func TestServerRecordVerifiedDiscoveryPeerRequiresDirectConfirmation(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:           "https://portal.example.com",
		Bootstraps:          []string{"https://bootstrap.example.com"},
		WireGuardPrivateKey: strings.Repeat("24", 32),
		DiscoveryPort:       41023,
		DiscoveryEnabled:    true,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	bootstrapDesc := mustRelayDescriptor(t, "https://bootstrap.example.com")
	relayADesc := mustRelayDescriptor(t, "https://relay-a.example.com")

	applyDiscovery := func(targetRelayID, targetURL string, resp types.DiscoveryResponse, requireSelfOverlay bool) (bool, int, error, error) {
		now := time.Now().UTC()
		if requireSelfOverlay {
			_, updated, added, warnErr, err := server.relaySet.ApplyOverlayRelayDiscoveryResponse(targetRelayID, targetURL, resp, now)
			return updated, added, warnErr, err
		}
		_, updated, added, warnErr, err := server.relaySet.ApplyRelayDiscoveryResponse(targetRelayID, targetURL, resp, now)
		return updated, added, warnErr, err
	}

	resultUpdated, resultAdded, warnErr, err := applyDiscovery(
		bootstrapDesc.RelayID,
		bootstrapDesc.APIHTTPSAddr,
		types.DiscoveryResponse{ProtocolVersion: types.ProtocolVersion, Self: bootstrapDesc},
		false,
	)
	if err != nil {
		t.Fatalf("applyRelayDiscoveryResponse() bootstrap error = %v", err)
	}
	if warnErr != nil {
		t.Fatalf("applyRelayDiscoveryResponse() bootstrap warn = %v, want nil", warnErr)
	}
	if resultAdded != 0 {
		t.Fatalf("applyRelayDiscoveryResponse() bootstrap added = %d, want 0 for seeded bootstrap", resultAdded)
	}
	if !resultUpdated {
		t.Fatal("applyRelayDiscoveryResponse() bootstrap updated = false, want true")
	}

	resultUpdated, resultAdded, warnErr, err = applyDiscovery(
		bootstrapDesc.RelayID,
		bootstrapDesc.APIHTTPSAddr,
		types.DiscoveryResponse{ProtocolVersion: types.ProtocolVersion, Self: bootstrapDesc, Relays: []types.RelayDescriptor{relayADesc}},
		false,
	)
	if err != nil {
		t.Fatalf("applyRelayDiscoveryResponse() hinted error = %v", err)
	}
	if warnErr != nil {
		t.Fatalf("applyRelayDiscoveryResponse() hinted warn = %v, want nil", warnErr)
	}
	if resultAdded != 1 {
		t.Fatalf("applyRelayDiscoveryResponse() hinted added = %d, want 1", resultAdded)
	}
	if !resultUpdated {
		t.Fatal("applyRelayDiscoveryResponse() hinted updated = false, want true")
	}
	snapshot := server.relaySet.Snapshot()
	if len(snapshot) != 2 {
		t.Fatalf("Snapshot() size = %d, want 2 after hinted relay registration", len(snapshot))
	}
	bootstrapDescriptors := server.relaySet.BootstrapDescriptors()
	knownURLs := make([]string, 0, len(bootstrapDescriptors))
	for _, descriptor := range bootstrapDescriptors {
		if strings.TrimSpace(descriptor.APIHTTPSAddr) == "" {
			continue
		}
		knownURLs = append(knownURLs, descriptor.APIHTTPSAddr)
	}
	sort.Strings(knownURLs)
	knownURLs, err = utils.ExcludeLocalRelayURLs(knownURLs...)
	if err != nil {
		t.Fatalf("ExcludeLocalRelayURLs() known error = %v", err)
	}
	if !reflect.DeepEqual(knownURLs, []string{"https://bootstrap.example.com"}) {
		t.Fatalf("BootstrapDescriptors() = %v, want [%q]", knownURLs, "https://bootstrap.example.com")
	}
	syncableDescriptors := server.relaySet.SyncableDescriptors()
	syncableURLs := make([]string, 0, len(syncableDescriptors))
	for _, descriptor := range syncableDescriptors {
		if strings.TrimSpace(descriptor.APIHTTPSAddr) == "" {
			continue
		}
		syncableURLs = append(syncableURLs, descriptor.APIHTTPSAddr)
	}
	sort.Strings(syncableURLs)
	syncableURLs, err = utils.ExcludeLocalRelayURLs(syncableURLs...)
	if err != nil {
		t.Fatalf("ExcludeLocalRelayURLs() syncable error = %v", err)
	}
	if !reflect.DeepEqual(syncableURLs, []string{"https://relay-a.example.com"}) {
		t.Fatalf("SyncablePeerDescriptors() = %v, want [%q]", syncableURLs, "https://relay-a.example.com")
	}
	advertisedDescriptors := server.relaySet.AdvertisedDescriptors()
	advertisedURLs := make([]string, 0, len(advertisedDescriptors))
	for _, descriptor := range advertisedDescriptors {
		if strings.TrimSpace(descriptor.APIHTTPSAddr) == "" {
			continue
		}
		advertisedURLs = append(advertisedURLs, descriptor.APIHTTPSAddr)
	}
	sort.Strings(advertisedURLs)
	advertisedURLs, err = utils.ExcludeLocalRelayURLs(advertisedURLs...)
	if err != nil {
		t.Fatalf("ExcludeLocalRelayURLs() advertised error = %v", err)
	}
	if !reflect.DeepEqual(advertisedURLs, []string{"https://bootstrap.example.com"}) {
		t.Fatalf("AdvertisedDescriptors() = %v, want [%q]", advertisedURLs, "https://bootstrap.example.com")
	}

	resultUpdated, resultAdded, warnErr, err = applyDiscovery(
		relayADesc.RelayID,
		relayADesc.APIHTTPSAddr,
		types.DiscoveryResponse{ProtocolVersion: types.ProtocolVersion, Self: relayADesc},
		true,
	)
	if err != nil {
		t.Fatalf("applyRelayDiscoveryResponse() confirm error = %v", err)
	}
	if warnErr != nil {
		t.Fatalf("applyRelayDiscoveryResponse() confirm warn = %v, want nil", warnErr)
	}
	if resultAdded != 0 {
		t.Fatalf("applyRelayDiscoveryResponse() confirm added = %d, want 0", resultAdded)
	}
	if !resultUpdated {
		t.Fatal("applyRelayDiscoveryResponse() confirm updated = false, want true")
	}
	advertisedDescriptors = server.relaySet.AdvertisedDescriptors()
	advertisedURLs = advertisedURLs[:0]
	for _, descriptor := range advertisedDescriptors {
		if strings.TrimSpace(descriptor.APIHTTPSAddr) == "" {
			continue
		}
		advertisedURLs = append(advertisedURLs, descriptor.APIHTTPSAddr)
	}
	sort.Strings(advertisedURLs)
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

	resp, err := client.Get("https://" + utils.HostPortOrLoopback(server.APIAddr()) + types.PathDiscovery)
	if err != nil {
		t.Fatalf("GET relay discovery error = %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET relay discovery status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	if server.DiscoveryEnabled() {
		t.Fatal("DiscoveryEnabled() = true, want false without configured discovery service")
	}
}
