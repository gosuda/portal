package manager

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
)

func TestIPManagerBanAndSetBannedIPs(t *testing.T) {
	t.Parallel()

	m := NewIPManager()

	m.BanIP("198.51.100.1")
	if !m.IsIPBanned("198.51.100.1") {
		t.Fatal("expected IP to be banned")
	}
	m.UnbanIP("198.51.100.1")
	if m.IsIPBanned("198.51.100.1") {
		t.Fatal("expected IP ban to be removed")
	}

	m.SetBannedIPs([]string{"203.0.113.1", "203.0.113.2", "203.0.113.1"})
	banned := m.GetBannedIPs()
	slices.Sort(banned)
	want := []string{"203.0.113.1", "203.0.113.2"}
	if !slices.Equal(banned, want) {
		t.Fatalf("GetBannedIPs() = %v, want %v", banned, want)
	}
}

func TestIPManagerRegisterLeaseIPAndLookup(t *testing.T) {
	t.Parallel()

	m := NewIPManager()

	m.RegisterLeaseIP("lease-a", "10.0.0.1")
	m.RegisterLeaseIP("lease-b", "10.0.0.1")
	if got := m.GetLeaseIP("lease-a"); got != "10.0.0.1" {
		t.Fatalf("GetLeaseIP(lease-a) = %q, want %q", got, "10.0.0.1")
	}

	leases := m.GetIPLeases("10.0.0.1")
	slices.Sort(leases)
	wantLeases := []string{"lease-a", "lease-b"}
	if !slices.Equal(leases, wantLeases) {
		t.Fatalf("GetIPLeases(10.0.0.1) = %v, want %v", leases, wantLeases)
	}

	m.RegisterLeaseIP("lease-a", "10.0.0.2")
	if got := m.GetLeaseIP("lease-a"); got != "10.0.0.2" {
		t.Fatalf("GetLeaseIP(lease-a) after update = %q, want %q", got, "10.0.0.2")
	}
	if got := m.GetIPLeases("10.0.0.1"); len(got) != 1 || got[0] != "lease-b" {
		t.Fatalf("GetIPLeases(10.0.0.1) after update = %v, want [lease-b]", got)
	}
	if got := m.GetIPLeases("10.0.0.2"); len(got) != 1 || got[0] != "lease-a" {
		t.Fatalf("GetIPLeases(10.0.0.2) after update = %v, want [lease-a]", got)
	}
}

func TestIPManagerGetIPLeasesReturnsCopy(t *testing.T) {
	t.Parallel()

	m := NewIPManager()
	m.RegisterLeaseIP("lease-a", "10.0.0.1")

	leases := m.GetIPLeases("10.0.0.1")
	if len(leases) != 1 {
		t.Fatalf("GetIPLeases() len = %d, want 1", len(leases))
	}
	leases[0] = "mutated"

	fresh := m.GetIPLeases("10.0.0.1")
	if len(fresh) != 1 || fresh[0] != "lease-a" {
		t.Fatalf("GetIPLeases() should not expose internal slice, got %v", fresh)
	}
}

func TestIPManagerPendingIPQueue(t *testing.T) {
	t.Parallel()

	m := NewIPManager()

	// Empty input is ignored.
	m.StorePendingIP("")
	if got := m.PopPendingIP(); got != "" {
		t.Fatalf("PopPendingIP() = %q, want empty for empty-input store", got)
	}

	// Queue keeps only the most recent pendingIPsMax items.
	const totalStored = 105
	for i := range totalStored {
		m.StorePendingIP(fmt.Sprintf("198.51.100.%03d", i))
	}

	first := m.PopPendingIP()
	if first != "198.51.100.005" {
		t.Fatalf("first popped IP = %q, want %q", first, "198.51.100.005")
	}

	count := 1
	for {
		ip := m.PopPendingIP()
		if ip == "" {
			break
		}
		count++
	}
	if count != m.pendingIPsMax {
		t.Fatalf("popped %d IPs, want %d", count, m.pendingIPsMax)
	}

	if got := m.PopPendingIP(); got != "" {
		t.Fatalf("PopPendingIP() on empty queue = %q, want empty", got)
	}
}

func TestExtractClientIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		xff        string
		xri        string
		remoteAddr string
		want       string
	}{
		{
			name:       "xff first IP wins",
			xff:        "198.51.100.1, 10.0.0.1",
			remoteAddr: "203.0.113.9:443",
			want:       "198.51.100.1",
		},
		{
			name:       "xff single IP is trimmed",
			xff:        " 198.51.100.2 ",
			remoteAddr: "203.0.113.9:443",
			want:       "198.51.100.2",
		},
		{
			name:       "x-real-ip fallback",
			xri:        " 203.0.113.7 ",
			remoteAddr: "203.0.113.9:443",
			want:       "203.0.113.7",
		},
		{
			name:       "remote addr hostport fallback",
			remoteAddr: "203.0.113.10:8443",
			want:       "203.0.113.10",
		},
		{
			name:       "remote addr malformed returns raw",
			remoteAddr: "malformed-addr",
			want:       "malformed-addr",
		},
		{
			name:       "xff takes precedence over x-real-ip",
			xff:        "198.51.100.99",
			xri:        "203.0.113.99",
			remoteAddr: "203.0.113.9:443",
			want:       "198.51.100.99",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "https://portal.example/relay", http.NoBody)
			req.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xri != "" {
				req.Header.Set("X-Real-IP", tt.xri)
			}

			if got := ExtractClientIP(req); got != tt.want {
				t.Fatalf("ExtractClientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIPManagerConcurrentRegisterLeaseIP(t *testing.T) {
	t.Parallel()

	const workers = 40
	m := NewIPManager()

	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		leaseID := fmt.Sprintf("lease-%02d", i)
		ip := fmt.Sprintf("203.0.113.%d", (i%4)+1)
		go func(id, addr string) {
			defer wg.Done()
			m.RegisterLeaseIP(id, addr)
		}(leaseID, ip)
	}
	wg.Wait()

	totalLeases := 0
	for i := range workers {
		leaseID := fmt.Sprintf("lease-%02d", i)
		if got := m.GetLeaseIP(leaseID); got == "" {
			t.Fatalf("lease %q has no IP mapping", leaseID)
		}
	}
	for i := 1; i <= 4; i++ {
		totalLeases += len(m.GetIPLeases(fmt.Sprintf("203.0.113.%d", i)))
	}
	if totalLeases != workers {
		t.Fatalf("total mapped leases = %d, want %d", totalLeases, workers)
	}
}
