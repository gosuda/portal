package sni

import (
	"errors"
	"net"
	"testing"
	"time"
)

func TestRouter_RegisterRoute(t *testing.T) {
	router := NewRouter("")

	// Test basic registration
	err := router.RegisterRoute("example.com", "lease-1", "test")
	if err != nil {
		t.Fatalf("failed to register route: %v", err)
	}

	// Test duplicate registration by same lease (should succeed - update)
	err = router.RegisterRoute("example.com", "lease-1", "test")
	if err != nil {
		t.Fatalf("failed to update route: %v", err)
	}

	route, ok := router.GetRoute("example.com")
	if !ok {
		t.Fatal("route not found")
	}
	if route.LeaseID != "lease-1" {
		t.Errorf("expected lease-1, got %s", route.LeaseID)
	}
}

func TestRouter_UnregisterRoute(t *testing.T) {
	router := NewRouter("")

	err := router.RegisterRoute("example.com", "lease-1", "test")
	if err != nil {
		t.Fatalf("failed to register route: %v", err)
	}

	router.UnregisterRoute("example.com")

	_, ok := router.GetRoute("example.com")
	if ok {
		t.Error("expected route to be unregistered")
	}
}

func TestRouter_UnregisterRouteByLeaseID(t *testing.T) {
	router := NewRouter("")

	err := router.RegisterRoute("example.com", "lease-1", "test")
	if err != nil {
		t.Fatalf("failed to register route: %v", err)
	}

	router.UnregisterRouteByLeaseID("lease-1")

	_, ok := router.GetRoute("example.com")
	if ok {
		t.Error("expected route to be unregistered")
	}
}

func TestRouter_GetRoute_Wildcard(t *testing.T) {
	router := NewRouter("")

	// Register wildcard route
	err := router.RegisterRoute("*.example.com", "lease-1", "test")
	if err != nil {
		t.Fatalf("failed to register wildcard route: %v", err)
	}

	tests := []struct {
		sni      string
		wantName string
		wantOK   bool
	}{
		{"foo.example.com", "*.example.com", true}, // should match
		{"bar.example.com", "*.example.com", true}, // should match
		{"example.com", "", false},                 // should NOT match (no subdomain)
		{"foo.bar.example.com", "", false},         // should NOT match (TLS wildcard only matches one level)
		{"other.com", "", false},                   // should NOT match
	}

	for _, tt := range tests {
		t.Run(tt.sni, func(t *testing.T) {
			route, ok := router.GetRoute(tt.sni)
			if ok != tt.wantOK {
				t.Errorf("GetRoute(%q) = %v, want %v", tt.sni, ok, tt.wantOK)
				return
			}
			if ok && route.SNI != tt.wantName {
				t.Errorf("GetRoute(%q) matched %q, want %q", tt.sni, route.SNI, tt.wantName)
			}
		})
	}
}

func TestRouter_GetRoute_ExactBeforeWildcard(t *testing.T) {
	router := NewRouter("")

	// Register both exact and wildcard routes
	err := router.RegisterRoute("*.example.com", "lease-1", "wildcard")
	if err != nil {
		t.Fatalf("failed to register wildcard route: %v", err)
	}

	err = router.RegisterRoute("specific.example.com", "lease-2", "specific")
	if err != nil {
		t.Fatalf("failed to register specific route: %v", err)
	}

	// Exact match should take precedence
	route, ok := router.GetRoute("specific.example.com")
	if !ok {
		t.Fatal("route not found")
	}
	if route.LeaseID != "lease-2" {
		t.Errorf("expected lease-2 (exact match), got %s", route.LeaseID)
	}

	// Other subdomains should match wildcard
	route, ok = router.GetRoute("other.example.com")
	if !ok {
		t.Fatal("route not found")
	}
	if route.LeaseID != "lease-1" {
		t.Errorf("expected lease-1 (wildcard match), got %s", route.LeaseID)
	}
}

func TestRouter_GetRouteByLeaseID(t *testing.T) {
	router := NewRouter("")

	err := router.RegisterRoute("example.com", "lease-1", "test")
	if err != nil {
		t.Fatalf("failed to register route: %v", err)
	}

	route, ok := router.GetRouteByLeaseID("lease-1")
	if !ok {
		t.Fatal("route not found by lease ID")
	}
	if route.SNI != "example.com" {
		t.Errorf("expected SNI example.com, got %s", route.SNI)
	}

	_, ok = router.GetRouteByLeaseID("nonexistent")
	if ok {
		t.Error("expected route not found for nonexistent lease ID")
	}
}

func TestRouter_GetAllRoutes(t *testing.T) {
	router := NewRouter("")

	_ = router.RegisterRoute("example.com", "lease-1", "test1")
	_ = router.RegisterRoute("other.com", "lease-2", "test2")

	routes := router.GetAllRoutes()
	if len(routes) != 2 {
		t.Errorf("expected 2 routes, got %d", len(routes))
	}
}

func TestRouter_CaseInsensitive(t *testing.T) {
	router := NewRouter("")

	err := router.RegisterRoute("Example.COM", "lease-1", "test")
	if err != nil {
		t.Fatalf("failed to register route: %v", err)
	}

	// Should find with different case
	route, ok := router.GetRoute("EXAMPLE.com")
	if !ok {
		t.Fatal("route not found with different case")
	}
	if route.SNI != "example.com" {
		t.Errorf("expected normalized SNI example.com, got %s", route.SNI)
	}
}

func TestRouter_LeaseRename(t *testing.T) {
	router := NewRouter("")

	// Register with name1
	err := router.RegisterRoute("name1.example.com", "lease-1", "name1")
	if err != nil {
		t.Fatalf("failed to register route: %v", err)
	}

	// Same lease re-registers with name2
	err = router.RegisterRoute("name2.example.com", "lease-1", "name2")
	if err != nil {
		t.Fatalf("failed to re-register route: %v", err)
	}

	// Old name should be gone
	_, ok := router.GetRoute("name1.example.com")
	if ok {
		t.Error("old route should be removed")
	}

	// New name should exist
	route, ok := router.GetRoute("name2.example.com")
	if !ok {
		t.Fatal("new route not found")
	}
	if route.LeaseID != "lease-1" {
		t.Errorf("expected lease-1, got %s", route.LeaseID)
	}
}

func TestRouter_Stop(t *testing.T) {
	router := NewRouter("")

	err := router.RegisterRoute("example.com", "lease-1", "test")
	if err != nil {
		t.Fatalf("failed to register route: %v", err)
	}

	// Stop should not panic
	err = router.Stop()
	if err != nil {
		t.Errorf("stop failed: %v", err)
	}

	// Registration after stop should fail
	err = router.RegisterRoute("other.com", "lease-2", "test2")
	if !errors.Is(err, ErrRouterClosed) {
		t.Errorf("expected ErrRouterClosed, got %v", err)
	}
}

func TestRouter_HandleConnectionNoRouteHandlerHandled(t *testing.T) {
	router := NewRouter("")
	noRouteCalls := make(chan string, 1)
	router.SetNoRouteHandler(func(_ net.Conn, sni string) bool {
		select {
		case noRouteCalls <- sni:
		default:
		}
		return true
	})

	client, server := net.Pipe()
	defer func() {
		_ = client.Close()
		_ = server.Close()
	}()

	done := make(chan struct{})
	router.wg.Add(1)
	go func() {
		router.handleConnection(server)
		close(done)
	}()

	if _, err := client.Write(buildClientHello("tenant.example.com", true)); err != nil {
		t.Fatalf("write client hello: %v", err)
	}

	select {
	case gotSNI := <-noRouteCalls:
		if gotSNI != "tenant.example.com" {
			t.Fatalf("no-route handler sni=%q, want %q", gotSNI, "tenant.example.com")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no-route handler was not called")
	}

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handleConnection did not return after handled no-route callback")
	}

	_ = client.SetReadDeadline(time.Now().Add(75 * time.Millisecond))
	var b [1]byte
	_, err := client.Read(b[:])
	if err == nil {
		t.Fatal("expected read timeout while connection remains open")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected timeout error to indicate open connection, got: %v", err)
	}
}

func TestRouter_HandleConnectionNoRouteHandlerDeclined(t *testing.T) {
	router := NewRouter("")
	noRouteCalls := make(chan string, 1)
	router.SetNoRouteHandler(func(_ net.Conn, sni string) bool {
		select {
		case noRouteCalls <- sni:
		default:
		}
		return false
	})

	client, server := net.Pipe()
	defer func() {
		_ = client.Close()
		_ = server.Close()
	}()

	done := make(chan struct{})
	router.wg.Add(1)
	go func() {
		router.handleConnection(server)
		close(done)
	}()

	if _, err := client.Write(buildClientHello("tenant.example.com", true)); err != nil {
		t.Fatalf("write client hello: %v", err)
	}

	select {
	case <-noRouteCalls:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no-route handler was not called")
	}

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("handleConnection did not return after declined no-route callback")
	}

	_ = client.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	var b [1]byte
	_, err := client.Read(b[:])
	if err == nil {
		t.Fatal("expected closed connection after declined no-route callback")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("expected closed-connection error, got timeout: %v", err)
	}
}

func TestCloseWithDebugLogNilCloser(_ *testing.T) {
	closeWithDebugLog(nil, "noop")
}
