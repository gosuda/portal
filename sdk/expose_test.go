package sdk

import (
	"net/url"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/portal/discovery"
	"github.com/gosuda/portal/v2/types"
)

func mustRelayDescriptor(t *testing.T, relayName, relayURL string) types.RelayDescriptor {
	t.Helper()

	now := time.Now().UTC()
	desc, err := discovery.NormalizeDescriptor(types.RelayDescriptor{
		Identity: types.Identity{
			Name: relayName,
		},
		Sequence:     uint64(now.UnixMilli()),
		Version:      1,
		IssuedAt:     now,
		ExpiresAt:    now.Add(time.Hour),
		APIHTTPSAddr: relayURL,
	})
	if err != nil {
		t.Fatalf("NormalizeDescriptor() error = %v", err)
	}
	return desc
}

func TestExposureBanRelayURLMovesRelay(t *testing.T) {
	const (
		relayA = "https://relay-a.example"
		relayB = "https://relay-b.example"
	)

	relayURL, err := url.Parse(relayA)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	listener := &Listener{
		api: &apiClient{baseURL: relayURL},
	}

	exposure := &Exposure{
		relaySet:       discovery.NewRelaySet(),
		relayListeners: make(map[string]*Listener, 2),
	}
	exposure.relaySet.ReplaceKnownRelayURLs([]string{relayA, relayB})
	exposure.relayListeners = map[string]*Listener{
		relayA: listener,
		relayB: {},
	}

	exposure.relaySet.BanRelayURL(relayA, "mitm")
	exposure.listenerMu.Lock()
	delete(exposure.relayListeners, relayA)
	exposure.listenerMu.Unlock()

	if got := exposure.ActiveRelayURLs(); len(got) != 1 || got[0] != relayB {
		t.Fatalf("ActiveRelayURLs() = %v, want [%q]", got, relayB)
	}

	knownRelayURLs := exposure.relaySet.ActiveRelayURLs()
	exposure.listenerMu.RLock()
	_, listenerExists := exposure.relayListeners[relayA]
	exposure.listenerMu.RUnlock()
	if len(knownRelayURLs) != 1 || knownRelayURLs[0] != relayB {
		t.Fatalf("knownRelayURLs = %v, want [%q]", knownRelayURLs, relayB)
	}
	if listenerExists {
		t.Fatal("banned relay listener still exists in exposure.listeners")
	}
}

func TestExposureSetRelayURLsSkipsBannedRelay(t *testing.T) {
	const (
		relayA = "https://relay-a.example"
		relayB = "https://relay-b.example"
	)

	exposure := &Exposure{
		relaySet:       discovery.NewRelaySet(),
		relayListeners: make(map[string]*Listener, 1),
	}
	exposure.relaySet.BanRelayURL(relayB, "test")
	exposure.relayListeners = map[string]*Listener{
		relayA: {},
	}

	exposure.relaySet.ReplaceKnownRelayURLs([]string{relayA, relayB})
	if err := exposure.reconcileRelayListeners(false); err != nil {
		t.Fatalf("reconcileRelayListeners() error = %v", err)
	}
	if got := exposure.ActiveRelayURLs(); len(got) != 1 || got[0] != relayA {
		t.Fatalf("ActiveRelayURLs() = %v, want [%q]", got, relayA)
	}
	knownRelayURLs := exposure.relaySet.ActiveRelayURLs()
	if len(knownRelayURLs) != 1 || knownRelayURLs[0] != relayA {
		t.Fatalf("knownRelayURLs = %v, want [%q]", knownRelayURLs, relayA)
	}
}

func TestExposureSetRelayURLsRemovesStaleListener(t *testing.T) {
	const (
		relayA = "https://relay-a.example"
		relayB = "https://relay-b.example"
	)

	relayAURL, err := url.Parse(relayA)
	if err != nil {
		t.Fatalf("url.Parse(relayA) error = %v", err)
	}
	relayBURL, err := url.Parse(relayB)
	if err != nil {
		t.Fatalf("url.Parse(relayB) error = %v", err)
	}

	relayAClosed := make(chan struct{})
	exposure := &Exposure{
		relaySet:       discovery.NewRelaySet(),
		relayListeners: make(map[string]*Listener, 2),
	}
	exposure.relaySet.ReplaceKnownRelayURLs([]string{relayA, relayB})
	exposure.relayListeners = map[string]*Listener{
		relayA: {
			api:    &apiClient{baseURL: relayAURL},
			cancel: func() { close(relayAClosed) },
			doneCh: relayAClosed,
		},
		relayB: {
			api: &apiClient{baseURL: relayBURL},
		},
	}

	exposure.relaySet.ReplaceKnownRelayURLs([]string{relayB})
	if err := exposure.reconcileRelayListeners(false); err != nil {
		t.Fatalf("reconcileRelayListeners() error = %v", err)
	}

	select {
	case <-relayAClosed:
	default:
		t.Fatal("stale relay listener was not closed")
	}

	knownRelayURLs := exposure.relaySet.ActiveRelayURLs()
	exposure.listenerMu.RLock()
	_, relayAExists := exposure.relayListeners[relayA]
	_, relayBExists := exposure.relayListeners[relayB]
	exposure.listenerMu.RUnlock()
	if len(knownRelayURLs) != 1 || knownRelayURLs[0] != relayB {
		t.Fatalf("knownRelayURLs = %v, want [%q]", knownRelayURLs, relayB)
	}
	if relayAExists {
		t.Fatal("stale relay listener still exists in exposure.listeners")
	}
	if !relayBExists {
		t.Fatal("active relay listener missing from exposure.listeners")
	}
}

func TestExposurePinDiscoveredDescriptorAllowsURLChangeForSameIdentity(t *testing.T) {
	exposure := &Exposure{relaySet: discovery.NewRelaySet()}
	desc := mustRelayDescriptor(t, "relay-a", "https://relay-a.example")

	if _, _, _, _, err := exposure.relaySet.ApplyRelayDiscoveryResponse(desc.Identity, desc.APIHTTPSAddr, types.DiscoveryResponse{ProtocolVersion: types.ProtocolVersion, Self: desc}, time.Now().UTC()); err != nil {
		t.Fatalf("ApplyRelayDiscoveryResponse() error = %v", err)
	}

	changedURL := mustRelayDescriptor(t, desc.Name, "https://relay-b.example")
	_, _, _, _, err := exposure.relaySet.ApplyRelayDiscoveryResponse(desc.Identity, "", types.DiscoveryResponse{ProtocolVersion: types.ProtocolVersion, Self: changedURL}, time.Now().UTC())
	if err != nil {
		t.Fatalf("ApplyRelayDiscoveryResponse() error = %v, want nil for same relay identity", err)
	}
}
