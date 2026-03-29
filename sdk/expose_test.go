package sdk

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/portal/v2/portal/discovery"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

func mustSignedRelayDescriptor(t *testing.T, ownerPrivateKey, relayID, relayURL string) types.RelayDescriptor {
	t.Helper()

	identity, err := utils.ResolveSecp256k1Identity(ownerPrivateKey)
	if err != nil {
		t.Fatalf("ResolveSecp256k1Identity() error = %v", err)
	}

	now := time.Now().UTC()
	desc, err := discovery.SignedDescriptor(types.RelayDescriptor{
		RelayID:         relayID,
		OwnerAddress:    identity.Address,
		SignerPublicKey: identity.PublicKey,
		Sequence:        uint64(now.UnixMilli()),
		Version:         1,
		IssuedAt:        now,
		ExpiresAt:       now.Add(time.Hour),
		APIHTTPSAddr:    relayURL,
		StatusState:     "healthy",
	}, identity.PrivateKey)
	if err != nil {
		t.Fatalf("SignedDescriptor() error = %v", err)
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

func TestExposurePinDiscoveredDescriptorRejectsIdentityChange(t *testing.T) {
	exposure := &Exposure{relaySet: discovery.NewRelaySet()}
	desc := mustSignedRelayDescriptor(t, strings.Repeat("11", 32), "relay-a", "https://relay-a.example")

	if _, _, _, _, err := exposure.relaySet.ApplyRelayDiscoveryResponse(desc.RelayID, desc.APIHTTPSAddr, types.DiscoveryResponse{Self: desc}, time.Now().UTC()); err != nil {
		t.Fatalf("ApplyRelayDiscoveryResponse() error = %v", err)
	}

	changedSigner := mustSignedRelayDescriptor(t, strings.Repeat("12", 32), desc.RelayID, desc.APIHTTPSAddr)
	_, _, _, _, err := exposure.relaySet.ApplyRelayDiscoveryResponse(desc.RelayID, desc.APIHTTPSAddr, types.DiscoveryResponse{Self: changedSigner}, time.Now().UTC())
	if err == nil {
		t.Fatal("ApplyRelayDiscoveryResponse() error = nil, want pinned signer mismatch")
	}

	changedURL := mustSignedRelayDescriptor(t, strings.Repeat("11", 32), desc.RelayID, "https://relay-b.example")
	_, _, _, _, err = exposure.relaySet.ApplyRelayDiscoveryResponse(desc.RelayID, "", types.DiscoveryResponse{Self: changedURL}, time.Now().UTC())
	if err == nil {
		t.Fatal("ApplyRelayDiscoveryResponse() error = nil, want pinned relay url mismatch")
	}
}
