package sdk

import (
	"net/url"
	"testing"

	"github.com/gosuda/portal/v2/types"
)

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
		api:           &apiClient{baseURL: relayURL},
		startupStatus: listenerStatusBanned,
	}

	exposure := &Exposure{
		knownRelayURLs:  []string{relayA, relayB},
		bannedRelayURLs: nil,
		listeners: map[string]*Listener{
			relayA: listener,
			relayB: {},
		},
	}

	exposure.banRelayURL(relayA)

	if got := exposure.ActiveRelayURLs(); len(got) != 1 || got[0] != relayB {
		t.Fatalf("ActiveRelayURLs() = %v, want [%q]", got, relayB)
	}

	exposure.mu.RLock()
	knownRelayURLs := append([]string(nil), exposure.knownRelayURLs...)
	bannedRelayURLs := append([]string(nil), exposure.bannedRelayURLs...)
	_, listenerExists := exposure.listeners[relayA]
	exposure.mu.RUnlock()
	if len(knownRelayURLs) != 1 || knownRelayURLs[0] != relayB {
		t.Fatalf("knownRelayURLs = %v, want [%q]", knownRelayURLs, relayB)
	}
	if len(bannedRelayURLs) != 1 || bannedRelayURLs[0] != relayA {
		t.Fatalf("bannedRelayURLs = %v, want [%q]", bannedRelayURLs, relayA)
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
		bannedRelayURLs: []string{relayB},
		listeners: map[string]*Listener{
			relayA: {},
		},
	}

	added, err := exposure.setRelayURLs([]string{relayA, relayB}, false)
	if err != nil {
		t.Fatalf("setRelayURLs() error = %v", err)
	}
	if len(added) != 1 || added[0] != relayA {
		t.Fatalf("added relay urls = %v, want [%q]", added, relayA)
	}
	if got := exposure.ActiveRelayURLs(); len(got) != 1 || got[0] != relayA {
		t.Fatalf("ActiveRelayURLs() = %v, want [%q]", got, relayA)
	}
	exposure.mu.RLock()
	knownRelayURLs := append([]string(nil), exposure.knownRelayURLs...)
	bannedRelayURLs := append([]string(nil), exposure.bannedRelayURLs...)
	exposure.mu.RUnlock()
	if len(knownRelayURLs) != 1 || knownRelayURLs[0] != relayA {
		t.Fatalf("knownRelayURLs = %v, want [%q]", knownRelayURLs, relayA)
	}
	if len(bannedRelayURLs) != 1 || bannedRelayURLs[0] != relayB {
		t.Fatalf("bannedRelayURLs = %v, want [%q]", bannedRelayURLs, relayB)
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
		knownRelayURLs: []string{relayA, relayB},
		listeners: map[string]*Listener{
			relayA: {
				api:    &apiClient{baseURL: relayAURL},
				cancel: func() { close(relayAClosed) },
				doneCh: relayAClosed,
			},
			relayB: {
				api: &apiClient{baseURL: relayBURL},
			},
		},
	}

	added, err := exposure.setRelayURLs([]string{relayB}, false)
	if err != nil {
		t.Fatalf("setRelayURLs() error = %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("added relay urls = %v, want empty", added)
	}

	select {
	case <-relayAClosed:
	default:
		t.Fatal("stale relay listener was not closed")
	}

	exposure.mu.RLock()
	knownRelayURLs := append([]string(nil), exposure.knownRelayURLs...)
	_, relayAExists := exposure.listeners[relayA]
	_, relayBExists := exposure.listeners[relayB]
	exposure.mu.RUnlock()
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
	exposure := &Exposure{}
	desc := types.RelayDescriptor{
		RelayID:         "relay-a",
		APIHTTPSAddr:    "https://relay-a.example",
		SignerPublicKey: "signer-a",
	}

	if err := exposure.pinDiscoverySelfDescriptor(desc.APIHTTPSAddr, desc); err != nil {
		t.Fatalf("pinDiscoverySelfDescriptor() error = %v", err)
	}

	changedSigner := desc
	changedSigner.SignerPublicKey = "signer-b"
	if err := exposure.pinDiscoveredDescriptor(changedSigner); err == nil {
		t.Fatal("pinDiscoveredDescriptor() error = nil, want pinned signer mismatch")
	}

	changedURL := desc
	changedURL.APIHTTPSAddr = "https://relay-b.example"
	if err := exposure.pinDiscoveredDescriptor(changedURL); err == nil {
		t.Fatal("pinDiscoveredDescriptor() error = nil, want pinned relay url mismatch")
	}
}
