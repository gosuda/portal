package wireguard

import (
	"encoding/base64"
	"net"
	"strings"
	"testing"

	"github.com/gosuda/portal/v2/utils"
)

func TestNormalizePrivateKeyAndPublicKeyFromPrivate(t *testing.T) {
	t.Parallel()

	privateKey, err := utils.NormalizeWireGuardPrivateKey("1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("NormalizeWireGuardPrivateKey() error = %v", err)
	}
	if _, err := base64.StdEncoding.DecodeString(privateKey); err != nil {
		t.Fatalf("NormalizeWireGuardPrivateKey() returned non-base64 key: %v", err)
	}

	publicKey, err := utils.WireGuardPublicKeyFromPrivate(privateKey)
	if err != nil {
		t.Fatalf("WireGuardPublicKeyFromPrivate() error = %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		t.Fatalf("WireGuardPublicKeyFromPrivate() returned non-base64 key: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("public key length = %d, want 32", len(decoded))
	}
}

func TestStackStartAndClose(t *testing.T) {
	t.Parallel()

	privateKey, err := utils.NormalizeWireGuardPrivateKey("2222222222222222222222222222222222222222222222222222222222222222")
	if err != nil {
		t.Fatalf("NormalizeWireGuardPrivateKey() error = %v", err)
	}

	port := reserveUDPPort(t)
	stack, err := newStack(Config{
		PrivateKey:  privateKey,
		Endpoint:    net.JoinHostPort("127.0.0.1", port),
		OverlayIPv4: "10.77.0.1",
	})
	if err != nil {
		t.Fatalf("newStack() error = %v", err)
	}
	t.Cleanup(func() {
		if err := stack.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
}

func TestResolvePeerEndpointPreservesIPLiteral(t *testing.T) {
	t.Parallel()

	got, err := resolvePeerEndpoint("127.0.0.1:51820")
	if err != nil {
		t.Fatalf("resolvePeerEndpoint() error = %v", err)
	}
	if got != "127.0.0.1:51820" {
		t.Fatalf("resolvePeerEndpoint() = %q, want %q", got, "127.0.0.1:51820")
	}
}

func TestResolvePeerEndpointResolvesHostname(t *testing.T) {
	t.Parallel()

	got, err := resolvePeerEndpoint("localhost:51820")
	if err != nil {
		t.Fatalf("resolvePeerEndpoint() error = %v", err)
	}

	host, port, err := net.SplitHostPort(got)
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	if port != "51820" {
		t.Fatalf("port = %q, want %q", port, "51820")
	}
	if strings.EqualFold(host, "localhost") {
		t.Fatalf("host = %q, want resolved IP literal", host)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		t.Fatalf("host = %q, want valid IP literal", host)
	}
	if !ip.IsLoopback() {
		t.Fatalf("host = %q, want loopback IP", host)
	}
}

func reserveUDPPort(t *testing.T) string {
	t.Helper()

	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket() error = %v", err)
	}
	defer conn.Close()

	_, port, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		t.Fatalf("SplitHostPort() error = %v", err)
	}
	return port
}
