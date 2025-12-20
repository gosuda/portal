//go:build js && wasm

package main

import (
	"encoding/base64"
	"encoding/hex"
	"io"
	"sync"
	"testing"
	"time"

	"syscall/js"

	"gosuda.org/portal/portal/core/cryptoops"
	"gosuda.org/portal/portal/core/proto/rdsec"
	"gosuda.org/portal/portal/core/proto/rdverb"
)

type mockConn struct {
	mu     sync.Mutex
	writes [][]byte
	closed bool
}

func (m *mockConn) Read(_ []byte) (int, error) {
	return 0, io.EOF
}

func (m *mockConn) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(p))
	copy(cp, p)
	m.writes = append(m.writes, cp)
	return len(p), nil
}

func (m *mockConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockConn) lastWrite() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.writes) == 0 {
		return nil
	}
	return m.writes[len(m.writes)-1]
}

func jsObject(fields map[string]interface{}) js.Value {
	obj := js.Global().Get("Object").New()
	for key, val := range fields {
		obj.Set(key, val)
	}
	return obj
}

func setGlobal(key string, val js.Value) func() {
	global := js.Global()
	prev := global.Get(key)
	hadPrev := !prev.IsUndefined() && !prev.IsNull()

	if val.IsUndefined() || val.IsNull() {
		global.Delete(key)
	} else {
		global.Set(key, val)
	}

	return func() {
		if hadPrev {
			global.Set(key, prev)
		} else {
			global.Delete(key)
		}
	}
}

func init() {
	// Default stub to avoid reliance on __sdk_post_message in wasm test runner.
	sdkPostMessage = func(_ map[string]interface{}) {}
}

func TestGetBootstrapServersDefaults(t *testing.T) {
	restore := setGlobal("__BOOTSTRAP_SERVERS__", js.Undefined())
	defer restore()

	got := getBootstrapServers()
	if len(got) != 1 || got[0] != "ws://localhost:4017/relay" {
		t.Fatalf("expected default bootstrap, got %#v", got)
	}
}

func TestGetBootstrapServersString(t *testing.T) {
	restore := setGlobal("__BOOTSTRAP_SERVERS__", js.ValueOf("ws://a, ws://b"))
	defer restore()

	got := getBootstrapServers()
	if len(got) != 2 || got[0] != "ws://a" || got[1] != "ws://b" {
		t.Fatalf("unexpected bootstrap list: %#v", got)
	}
}

func TestGetBootstrapServersArray(t *testing.T) {
	restore := setGlobal("__BOOTSTRAP_SERVERS__", js.ValueOf([]interface{}{"ws://a", "ws://b"}))
	defer restore()

	got := getBootstrapServers()
	if len(got) != 2 || got[0] != "ws://a" || got[1] != "ws://b" {
		t.Fatalf("unexpected bootstrap list: %#v", got)
	}
}

func TestGetBootstrapServersInvalid(t *testing.T) {
	restore := setGlobal("__BOOTSTRAP_SERVERS__", js.ValueOf(123))
	defer restore()

	got := getBootstrapServers()
	if len(got) != 1 || got[0] != "ws://localhost:4017/relay" {
		t.Fatalf("expected default bootstrap, got %#v", got)
	}
}

func TestDNSCacheLookupStore(t *testing.T) {
	origTTL := dnsCacheTTL
	dnsCacheTTL = time.Minute
	defer func() { dnsCacheTTL = origTTL }()

	dnsCache.Lock()
	dnsCache.cache = make(map[string]dnsCacheEntry)
	dnsCache.Unlock()

	storeDNSCache("TEST", "LEASE")
	if got, ok := lookupDNSCache("TEST"); !ok || got != "LEASE" {
		t.Fatalf("expected cache hit, got %v %v", got, ok)
	}

	dnsCache.Lock()
	entry := dnsCache.cache["TEST"]
	entry.expiresAt = time.Now().Add(-time.Second)
	dnsCache.cache["TEST"] = entry
	dnsCache.Unlock()

	if _, ok := lookupDNSCache("TEST"); ok {
		t.Fatalf("expected expired entry to be removed")
	}
}

func TestIsValidUpgradeRequest(t *testing.T) {
	valid := []byte("GET /ws HTTP/1.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
	if !isValidUpgradeRequest(valid) {
		t.Fatalf("expected valid upgrade request")
	}

	invalid := []byte("POST /ws HTTP/1.1\r\n\r\n")
	if isValidUpgradeRequest(invalid) {
		t.Fatalf("expected invalid upgrade request")
	}
}

func TestGenerateConnID(t *testing.T) {
	id := generateConnID()
	if len(id) != 32 {
		t.Fatalf("expected 32 hex chars, got %q", id)
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Fatalf("expected hex string, got %q", id)
	}
	id2 := generateConnID()
	if id == id2 {
		t.Fatalf("expected different IDs, got same %q", id)
	}
}

func TestGetLeaseID(t *testing.T) {
	if got := getLeaseID("foo.bar.com"); got != "FOO" {
		t.Fatalf("expected FOO, got %q", got)
	}
	if got := getLeaseID("  baz "); got != "BAZ" {
		t.Fatalf("expected BAZ, got %q", got)
	}
}

func TestHandleSDKSendBase64(t *testing.T) {
	conn := &mockConn{}
	connID := "conn-base64"

	sdkConnectionsMu.Lock()
	sdkConnections[connID] = conn
	sdkConnectionsMu.Unlock()
	defer func() {
		sdkConnectionsMu.Lock()
		delete(sdkConnections, connID)
		sdkConnectionsMu.Unlock()
	}()

	payload := []byte("hello")
	b64 := base64.StdEncoding.EncodeToString(payload)

	data := jsObject(map[string]interface{}{
		"connId": connID,
		"data":   b64,
	})
	handleSDKSend(data)

	if got := conn.lastWrite(); string(got) != "hello" {
		t.Fatalf("expected write %q, got %q", "hello", string(got))
	}
}

func TestHandleSDKSendUint8Array(t *testing.T) {
	conn := &mockConn{}
	connID := "conn-bytes"

	sdkConnectionsMu.Lock()
	sdkConnections[connID] = conn
	sdkConnectionsMu.Unlock()
	defer func() {
		sdkConnectionsMu.Lock()
		delete(sdkConnections, connID)
		sdkConnectionsMu.Unlock()
	}()

	payload := []byte("bytes")
	array := js.Global().Get("Uint8Array").New(len(payload))
	js.CopyBytesToJS(array, payload)

	data := jsObject(map[string]interface{}{
		"connId": connID,
		"data":   array,
	})
	handleSDKSend(data)

	if got := conn.lastWrite(); string(got) != "bytes" {
		t.Fatalf("expected write %q, got %q", "bytes", string(got))
	}
}

func TestHandleSDKClose(t *testing.T) {
	conn := &mockConn{}
	connID := "conn-close"

	sdkConnectionsMu.Lock()
	sdkConnections[connID] = conn
	sdkConnectionsMu.Unlock()

	data := jsObject(map[string]interface{}{
		"connId": connID,
	})
	handleSDKClose(data)

	sdkConnectionsMu.RLock()
	_, ok := sdkConnections[connID]
	sdkConnectionsMu.RUnlock()
	if ok {
		t.Fatalf("expected connection removed")
	}
	if !conn.closed {
		t.Fatalf("expected connection closed")
	}
}

func TestHandleSDKConnectMissingFields(t *testing.T) {
	// Should not panic on missing fields
	handleSDKConnect(jsObject(map[string]interface{}{
		"leaseName": "missing-client",
	}))
	handleSDKConnect(jsObject(map[string]interface{}{
		"clientId": "missing-lease",
	}))
}

func TestHandleSDKConnectSuccess(t *testing.T) {
	origLookup := sdkLookupLease
	origDial := sdkDialLease
	origCred := sdkNewCredential
	origPost := sdkPostMessage
	defer func() {
		sdkLookupLease = origLookup
		sdkDialLease = origDial
		sdkNewCredential = origCred
		sdkPostMessage = origPost
	}()

	lease := &rdverb.Lease{
		Identity: &rdsec.Identity{Id: "lease-1"},
		Name:     "TEST",
	}

	msgCh := make(chan map[string]interface{}, 2)
	sdkPostMessage = func(payload map[string]interface{}) {
		msgCh <- payload
	}
	sdkLookupLease = func(name string) (*rdverb.Lease, error) {
		if name != "TEST" {
			t.Fatalf("expected normalized lease name TEST, got %q", name)
		}
		return lease, nil
	}
	mock := &mockConn{}
	sdkDialLease = func(_ *cryptoops.Credential, leaseID string, alpn string) (io.ReadWriteCloser, error) {
		if leaseID != "lease-1" || alpn != "http/1.1" {
			t.Fatalf("unexpected dial args lease=%q alpn=%q", leaseID, alpn)
		}
		return mock, nil
	}
	sdkNewCredential = func() *cryptoops.Credential {
		return &cryptoops.Credential{}
	}

	upgrade := []byte("GET / HTTP/1.1\r\nUpgrade: websocket\r\n\r\n")
	array := js.Global().Get("Uint8Array").New(len(upgrade))
	js.CopyBytesToJS(array, upgrade)

	data := jsObject(map[string]interface{}{
		"leaseName":      "test",
		"clientId":       "client-1",
		"upgradeRequest": array,
	})
	handleSDKConnect(data)

	select {
	case msg := <-msgCh:
		if msg["type"] != "SDK_CONNECT_SUCCESS" {
			t.Fatalf("expected SDK_CONNECT_SUCCESS, got %#v", msg)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for SDK_CONNECT_SUCCESS")
	}

	sdkConnectionsMu.Lock()
	sdkConnections = make(map[string]io.ReadWriteCloser)
	sdkConnectionsMu.Unlock()
}
