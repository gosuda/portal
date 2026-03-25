package portal

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gosuda/portal/v2/portal/acme"
	"github.com/gosuda/portal/v2/portal/discovery"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

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
	if _, err := server.registerLease(types.RegisterRequest{
		Name:         "demo",
		ReverseToken: "tok_demo",
		Bootstraps:   []string{"https://relay-a.example.com", "https://bootstrap.example.com"},
	}, "203.0.113.10"); err != nil {
		t.Fatalf("registerLease() error = %v", err)
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
	if !envelope.Data.Found {
		t.Fatalf("resolve found = %v, want true", envelope.Data.Found)
	}
	if envelope.Data.OwnerAddress != ownerIdentity.Address {
		t.Fatalf("resolve owner address = %q, want relay owner address", envelope.Data.OwnerAddress)
	}
	if envelope.Data.Hostname != "demo.localhost" {
		t.Fatalf("resolve hostname = %q, want %q", envelope.Data.Hostname, "demo.localhost")
	}
	if !reflect.DeepEqual(envelope.Data.Bootstraps, []string{"https://bootstrap.example.com", "https://relay-a.example.com"}) {
		t.Fatalf("resolve bootstraps = %v, want [%q %q]", envelope.Data.Bootstraps, "https://bootstrap.example.com", "https://relay-a.example.com")
	}
	if !server.DiscoveryEnabled() {
		t.Fatal("DiscoveryEnabled() = false, want true")
	}
}

func TestServerMergeDiscoveryBootstrapsSkipsLocalRelayHosts(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:        "https://portal.example.com",
		Bootstraps:       []string{"https://bootstrap.example.com"},
		DiscoveryEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	added, err := server.mergeDiscoveryBootstraps([]string{
		"https://localhost:4017",
		"https://relay-a.example.com",
		"https://127.0.0.1:4017",
	})
	if err != nil {
		t.Fatalf("mergeDiscoveryBootstraps() error = %v", err)
	}

	if !reflect.DeepEqual(added, []string{"https://relay-a.example.com"}) {
		t.Fatalf("mergeDiscoveryBootstraps() added = %v, want [%q]", added, "https://relay-a.example.com")
	}
	if !reflect.DeepEqual(server.discoveryBootstrapsSnapshot(), []string{"https://bootstrap.example.com", "https://relay-a.example.com"}) {
		t.Fatalf("discoveryBootstrapsSnapshot() = %v, want [%q %q]", server.discoveryBootstrapsSnapshot(), "https://bootstrap.example.com", "https://relay-a.example.com")
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
