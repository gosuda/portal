package portal

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gosuda/portal/v2/portal/acme"
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
		UDPEnabled:    true,
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
		UDPEnabled:    true,
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
		PortalURL:  "https://portal.example.com",
		UDPEnabled: true,
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

func TestRegisterLeaseRejectsInvalidName(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:  "https://portal.example.com",
		UDPEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	_, err = server.registerLease(types.RegisterRequest{
		Name:         "demo app",
		ReverseToken: "tok_1",
	}, "203.0.113.10")
	if err == nil {
		t.Fatal("registerLease() error = nil, want invalid name error")
	}
}

func TestRegisterLeaseBuildsUDPEnabledRuntime(t *testing.T) {
	t.Parallel()

	server, err := NewServer(ServerConfig{
		PortalURL:  "https://portal.example.com",
		UDPEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	resp, err := server.registerLease(types.RegisterRequest{
		Name:         "demo-udp",
		ReverseToken: "tok_udp",
		UDPEnabled:   true,
	}, "203.0.113.10")
	if err != nil {
		t.Fatalf("registerLease() error = %v", err)
	}

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
