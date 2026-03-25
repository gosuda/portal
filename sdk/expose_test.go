package sdk

import (
	"net/url"
	"testing"
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
		activeRelayURLs: []string{relayA, relayB},
		bannedRelayURLs: nil,
		listeners: map[string]*Listener{
			relayA: listener,
			relayB: {},
		},
		starting: map[string]struct{}{
			relayA: {},
		},
	}

	exposure.banRelayURL(relayA)

	if got := exposure.KnownRelayURLs(); len(got) != 1 || got[0] != relayB {
		t.Fatalf("KnownRelayURLs() = %v, want [%q]", got, relayB)
	}
	if got := exposure.ActiveRelayURLs(); len(got) != 1 || got[0] != relayB {
		t.Fatalf("ActiveRelayURLs() = %v, want [%q]", got, relayB)
	}
	if got := exposure.BannedRelayURLs(); len(got) != 1 || got[0] != relayA {
		t.Fatalf("BannedRelayURLs() = %v, want [%q]", got, relayA)
	}

	exposure.mu.RLock()
	_, listenerExists := exposure.listeners[relayA]
	_, startingExists := exposure.starting[relayA]
	exposure.mu.RUnlock()
	if listenerExists {
		t.Fatal("banned relay listener still exists in exposure.listeners")
	}
	if startingExists {
		t.Fatal("banned relay still exists in exposure.starting")
	}
}

func TestExposureApplyRelayURLsSkipsBannedRelay(t *testing.T) {
	const (
		relayA = "https://relay-a.example"
		relayB = "https://relay-b.example"
	)

	exposure := &Exposure{
		bannedRelayURLs: []string{relayB},
		listeners: map[string]*Listener{
			relayA: {},
		},
		starting: make(map[string]struct{}),
	}

	added, err := exposure.applyRelayURLs([]string{relayA, relayB}, false)
	if err != nil {
		t.Fatalf("applyRelayURLs() error = %v", err)
	}
	if len(added) != 1 || added[0] != relayA {
		t.Fatalf("added relay urls = %v, want [%q]", added, relayA)
	}
	if got := exposure.KnownRelayURLs(); len(got) != 1 || got[0] != relayA {
		t.Fatalf("KnownRelayURLs() = %v, want [%q]", got, relayA)
	}
	if got := exposure.ActiveRelayURLs(); len(got) != 1 || got[0] != relayA {
		t.Fatalf("ActiveRelayURLs() = %v, want [%q]", got, relayA)
	}
	if got := exposure.BannedRelayURLs(); len(got) != 1 || got[0] != relayB {
		t.Fatalf("BannedRelayURLs() = %v, want [%q]", got, relayB)
	}
}
