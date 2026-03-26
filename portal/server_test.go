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

	identity, err := discovery.ResolveIdentity(ownerPrivateKey)
	if err != nil {
		t.Fatalf("ResolveIdentity() error = %v", err)
	}

	now := time.Now().UTC()
	desc, err := discovery.SignedDescriptor(types.RelayDescriptor{
		RelayID:         relayURL,
		OwnerAddress:    identity.Address,
		SignerPublicKey: identity.PublicKey,
		Sequence:        uint64(now.UnixMilli()),
		Version:         1,
		IssuedAt:        now,
		ExpiresAt:       now.Add(time.Hour),
		APIHTTPSAddr:    relayURL,
		SupportsTCP:     true,
		StatusState:     "healthy",
	}, identity.PrivateKey)
	if err != nil {
		t.Fatalf("SignedDescriptor() error = %v", err)
	}
	return desc
}

func mustRelayAPIURLs(t *testing.T, descriptors []types.RelayDescriptor) []string {
	t.Helper()

	urls, err := discovery.RelayAPIURLs(descriptors)
	if err != nil {
		t.Fatalf("RelayAPIURLs() error = %v", err)
	}
	return urls
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

	if server.cfg.WireGuardPrivateKey == "" {
		t.Fatal("WireGuardPrivateKey = empty, want normalized key")
	}
	if server.cfg.WireGuardPublicKey == "" {
		t.Fatal("WireGuardPublicKey = empty, want derived key")
	}
	if server.cfg.WireGuardEndpoint != net.JoinHostPort("portal.example.com", "41011") {
		t.Fatalf("WireGuardEndpoint = %q, want %q", server.cfg.WireGuardEndpoint, net.JoinHostPort("portal.example.com", "41011"))
	}
	if server.cfg.OverlayIPv4 == "" {
		t.Fatal("OverlayIPv4 = empty, want derived overlay address")
	}
	if err := discovery.ValidateWireGuardEndpoint(server.cfg.WireGuardEndpoint); err != nil {
		t.Fatalf("ValidateWireGuardEndpoint() error = %v", err)
	}
	if err := discovery.ValidateOverlayIPv4(server.cfg.OverlayIPv4); err != nil {
		t.Fatalf("ValidateOverlayIPv4() error = %v", err)
	}

	wantOverlay, err := utils.DeriveWireGuardOverlayIPv4(server.cfg.WireGuardPublicKey)
	if err != nil {
		t.Fatalf("DeriveWireGuardOverlayIPv4() error = %v", err)
	}
	if server.cfg.OverlayIPv4 != wantOverlay {
		t.Fatalf("OverlayIPv4 = %q, want %q", server.cfg.OverlayIPv4, wantOverlay)
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
	if server.cfg.WireGuardEndpoint != "" {
		t.Fatalf("WireGuardEndpoint = %q, want empty without wireguard key", server.cfg.WireGuardEndpoint)
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
			server.closeLease(record)
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

func TestServerStartServesOptionalDiscoveryRoutes(t *testing.T) {
	t.Parallel()

	ownerPrivateKey := strings.Repeat("11", 32)
	ownerIdentity, err := discovery.ResolveIdentity(ownerPrivateKey)
	if err != nil {
		t.Fatalf("ResolveIdentity() error = %v", err)
	}

	server, err := NewServer(ServerConfig{
		PortalURL:        "https://localhost:4017",
		OwnerPrivateKey:  ownerPrivateKey,
		Bootstraps:       []string{"https://bootstrap.example.com"},
		ACME:             acme.Config{KeyDir: t.TempDir()},
		APIListenAddr:    "127.0.0.1:0",
		SNIListenAddr:    "127.0.0.1:0",
		DiscoveryEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	registerResp, err := server.registerLease(types.RegisterRequest{
		Name:         "demo",
		ReverseToken: "tok_demo",
		Bootstraps:   []string{"https://relay-a.example.com", "https://bootstrap.example.com"},
	}, "203.0.113.10")
	if err != nil {
		t.Fatalf("registerLease() error = %v", err)
	}
	if !reflect.DeepEqual(registerResp.Bootstraps, []string{server.PortalURL()}) {
		t.Fatalf("registerLease() bootstraps = %v, want [%q]", registerResp.Bootstraps, server.PortalURL())
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

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET discovery resolve status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var envelope types.APIEnvelope[types.DiscoverResponse]
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode discovery resolve response: %v", err)
	}
	if !envelope.OK {
		t.Fatalf("discovery resolve envelope = %+v, want ok", envelope)
	}
	if envelope.Data.ProtocolVersion != 1 {
		t.Fatalf("resolve protocol_version = %d, want 1", envelope.Data.ProtocolVersion)
	}
	if envelope.Data.GeneratedAt.IsZero() {
		t.Fatal("resolve generated_at = zero, want timestamp")
	}
	if envelope.Data.Service == nil || !envelope.Data.Service.Found {
		t.Fatalf("resolve service = %+v, want found=true", envelope.Data.Service)
	}
	if envelope.Data.Service.OwnerAddress != ownerIdentity.Address {
		t.Fatalf("resolve service owner address = %q, want relay owner address", envelope.Data.Service.OwnerAddress)
	}
	if envelope.Data.Service.Hostname != "demo.localhost" {
		t.Fatalf("resolve service hostname = %q, want %q", envelope.Data.Service.Hostname, "demo.localhost")
	}
	if envelope.Data.Service.RelayID != envelope.Data.Self.RelayID {
		t.Fatalf("resolve service relay_id = %q, want %q", envelope.Data.Service.RelayID, envelope.Data.Self.RelayID)
	}

	if _, err := discovery.ValidateDescriptor(envelope.Data.Self, time.Now().UTC()); err != nil {
		t.Fatalf("ValidateDescriptor(self) error = %v", err)
	}
	relayURLs := make([]string, 0, 1+len(envelope.Data.Peers))
	relayURLs = append(relayURLs, envelope.Data.Self.APIHTTPSAddr)
	for _, peer := range envelope.Data.Peers {
		if strings.TrimSpace(peer.APIHTTPSAddr) != "" {
			relayURLs = append(relayURLs, peer.APIHTTPSAddr)
		}
	}
	if !reflect.DeepEqual(relayURLs, []string{server.PortalURL()}) {
		t.Fatalf("resolve relay urls = %v, want [%q]", relayURLs, server.PortalURL())
	}
	if envelope.Data.Self.OwnerAddress != ownerIdentity.Address {
		t.Fatalf("self relay owner address = %q, want %q", envelope.Data.Self.OwnerAddress, ownerIdentity.Address)
	}
	if envelope.Data.Self.SignerPublicKey != ownerIdentity.PublicKey {
		t.Fatalf("self relay signer public key = %q, want %q", envelope.Data.Self.SignerPublicKey, ownerIdentity.PublicKey)
	}
	if envelope.Data.Self.StatusState != "healthy" {
		t.Fatalf("self relay status_state = %q, want %q", envelope.Data.Self.StatusState, "healthy")
	}
	if !server.DiscoveryEnabled() {
		t.Fatal("DiscoveryEnabled() = false, want true")
	}
}

func TestServerUpsertDiscoverySeedURLsSkipsLocalRelayHosts(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:        "https://portal.example.com",
		Bootstraps:       []string{"https://bootstrap.example.com"},
		DiscoveryEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	added, err := server.discoveryCache.UpsertSeedURLs([]string{
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
	if !reflect.DeepEqual(mustRelayAPIURLs(t, server.discoveryCache.KnownDescriptors()), []string{"https://bootstrap.example.com", "https://relay-a.example.com"}) {
		t.Fatalf("KnownDescriptors() = %v, want [%q %q]", mustRelayAPIURLs(t, server.discoveryCache.KnownDescriptors()), "https://bootstrap.example.com", "https://relay-a.example.com")
	}
	if len(server.discoveryCache.AdvertisedDescriptors()) != 0 {
		t.Fatalf("AdvertisedDescriptors() = %v, want empty before direct confirmation", server.discoveryCache.AdvertisedDescriptors())
	}
}

func TestServerRecordVerifiedDiscoveryPeerRequiresDirectConfirmation(t *testing.T) {
	t.Parallel()

	ownerPrivateKey := strings.Repeat("11", 32)
	server, err := NewServer(ServerConfig{
		PortalURL:        "https://portal.example.com",
		Bootstraps:       []string{"https://bootstrap.example.com"},
		OwnerPrivateKey:  ownerPrivateKey,
		DiscoveryEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	bootstrapDesc := mustSignedRelayDescriptor(t, ownerPrivateKey, "https://bootstrap.example.com")
	relayADesc := mustSignedRelayDescriptor(t, ownerPrivateKey, "https://relay-a.example.com")

	added, changed, err := server.discoveryCache.RecordVerified(bootstrapDesc, true)
	if err != nil {
		t.Fatalf("RecordVerified() error = %v", err)
	}
	if added {
		t.Fatal("RecordVerified() added = true, want false for seeded bootstrap")
	}
	if !changed {
		t.Fatal("RecordVerified() changed = false, want true")
	}

	added, changed, err = server.discoveryCache.RecordVerified(relayADesc, false)
	if err != nil {
		t.Fatalf("RecordVerified() hinted error = %v", err)
	}
	if !added {
		t.Fatal("RecordVerified() hinted add = false, want true")
	}
	if !changed {
		t.Fatal("RecordVerified() hinted changed = false, want true")
	}
	if !reflect.DeepEqual(mustRelayAPIURLs(t, server.discoveryCache.KnownDescriptors()), []string{"https://bootstrap.example.com", "https://relay-a.example.com"}) {
		t.Fatalf("KnownDescriptors() = %v, want [%q %q]", mustRelayAPIURLs(t, server.discoveryCache.KnownDescriptors()), "https://bootstrap.example.com", "https://relay-a.example.com")
	}
	if !reflect.DeepEqual(mustRelayAPIURLs(t, server.discoveryCache.AdvertisedDescriptors()), []string{"https://bootstrap.example.com"}) {
		t.Fatalf("AdvertisedDescriptors() = %v, want [%q]", mustRelayAPIURLs(t, server.discoveryCache.AdvertisedDescriptors()), "https://bootstrap.example.com")
	}

	snapshot := server.discoveryCache.Snapshot()
	if snapshot[bootstrapDesc.RelayID].State != types.PeerStateAdvertised {
		t.Fatalf("bootstrap state = %q, want %q", snapshot[bootstrapDesc.RelayID].State, types.PeerStateAdvertised)
	}
	if snapshot[relayADesc.RelayID].State != types.PeerStateVerified {
		t.Fatalf("relay-a state = %q, want %q", snapshot[relayADesc.RelayID].State, types.PeerStateVerified)
	}

	added, changed, err = server.discoveryCache.RecordVerified(relayADesc, true)
	if err != nil {
		t.Fatalf("RecordVerified() second error = %v", err)
	}
	if added {
		t.Fatal("RecordVerified() second add = true, want false")
	}
	if !changed {
		t.Fatal("RecordVerified() second changed = false, want true")
	}
	if !reflect.DeepEqual(mustRelayAPIURLs(t, server.discoveryCache.AdvertisedDescriptors()), []string{"https://bootstrap.example.com", "https://relay-a.example.com"}) {
		t.Fatalf("AdvertisedDescriptors() = %v, want [%q %q]", mustRelayAPIURLs(t, server.discoveryCache.AdvertisedDescriptors()), "https://bootstrap.example.com", "https://relay-a.example.com")
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
