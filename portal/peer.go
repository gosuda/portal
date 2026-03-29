package portal

import (
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

type peerRecord struct {
	bootstrap             bool
	seedURL               string
	pinnedSignerPublicKey string
	state                 types.PeerState
}

// peerRegistry keeps only verified relay descriptors and bootstrap hints.
// It is not a source of truth; trust comes from seed URLs plus signer pinning.
type peerRegistry struct {
	mu    sync.RWMutex
	peers map[string]peerRecord
}

type peerRegistrationResult struct {
	updated        bool
	addedHintCount int
	peerSetChanged bool
}

type peerFailureOutcome struct {
	expired             bool
	expireReason        string
	consecutiveFailures int
}

func newPeerRegistry() *peerRegistry {
	return &peerRegistry{
		peers: make(map[string]peerRecord),
	}
}

func (r *peerRegistry) lookup(relayID string) (types.PeerState, bool, bool) {
	if strings.TrimSpace(relayID) == "" {
		return types.PeerState{}, false, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	record, ok := r.peers[relayID]
	if !ok {
		return types.PeerState{}, false, false
	}
	return record.state, strings.TrimSpace(record.pinnedSignerPublicKey) != "", true
}

func seedDescriptor(apiURL string) (types.RelayDescriptor, error) {
	normalized, err := utils.NormalizeRelayURL(apiURL)
	if err != nil {
		return types.RelayDescriptor{}, err
	}
	return types.RelayDescriptor{
		RelayID:      normalized,
		APIHTTPSAddr: normalized,
		Version:      1,
	}, nil
}

func requireOverlayPeerDescriptor(desc types.RelayDescriptor) error {
	if !desc.SupportsOverlayPeer {
		return errors.New("descriptor does not support overlay peer")
	}
	if strings.TrimSpace(desc.WireGuardPublicKey) == "" {
		return errors.New("descriptor wireguard public key is required")
	}
	if strings.TrimSpace(desc.WireGuardEndpoint) == "" {
		return errors.New("descriptor wireguard endpoint is required")
	}
	if strings.TrimSpace(desc.OverlayIPv4) == "" {
		return errors.New("descriptor overlay ipv4 is required")
	}
	return nil
}

func (r *peerRegistry) registerBootstrapURLs(inputs []string) ([]string, error) {
	if len(inputs) == 0 {
		return nil, nil
	}

	normalized, err := utils.NormalizeRelayURLs(inputs...)
	if err != nil {
		return nil, err
	}
	normalized, err = utils.ExcludeLocalRelayURLs(normalized...)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	added := make([]string, 0, len(normalized))

	r.mu.Lock()
	defer r.mu.Unlock()

	for _, apiURL := range normalized {
		descriptor, err := seedDescriptor(apiURL)
		if err != nil {
			return nil, err
		}

		record, ok := r.peers[descriptor.RelayID]
		if !ok {
			r.peers[descriptor.RelayID] = peerRecord{
				bootstrap: true,
				seedURL:   descriptor.APIHTTPSAddr,
				state: types.PeerState{
					Descriptor:  descriptor,
					State:       types.PeerStateKnown,
					FirstSeenAt: now,
					LastSeenAt:  now,
				},
			}
			added = append(added, descriptor.APIHTTPSAddr)
			continue
		}

		record.bootstrap = true
		if strings.TrimSpace(record.seedURL) == "" {
			record.seedURL = descriptor.APIHTTPSAddr
		}
		if strings.TrimSpace(record.state.Descriptor.APIHTTPSAddr) == "" {
			record.state.Descriptor.APIHTTPSAddr = descriptor.APIHTTPSAddr
		}
		if strings.TrimSpace(record.state.Descriptor.RelayID) == "" {
			record.state.Descriptor.RelayID = descriptor.RelayID
		}
		record.state.LastSeenAt = now
		r.peers[descriptor.RelayID] = record
	}

	return added, nil
}

func (r *peerRegistry) snapshot() map[string]types.PeerState {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string]types.PeerState, len(r.peers))
	for relayID, record := range r.peers {
		out[relayID] = record.state
	}
	return out
}

func (r *peerRegistry) bootstrapPeers() []types.RelayDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]types.RelayDescriptor, 0, len(r.peers))
	for _, record := range r.peers {
		if !record.bootstrap || strings.TrimSpace(record.state.Descriptor.APIHTTPSAddr) == "" {
			continue
		}
		out = append(out, record.state.Descriptor)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].APIHTTPSAddr < out[j].APIHTTPSAddr
	})
	return out
}

func (r *peerRegistry) advertisedPeers() []types.RelayDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]types.RelayDescriptor, 0, len(r.peers))
	for _, record := range r.peers {
		if record.state.State != types.PeerStateAdvertised {
			continue
		}
		out = append(out, record.state.Descriptor)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].APIHTTPSAddr < out[j].APIHTTPSAddr
	})
	return out
}

func (r *peerRegistry) syncablePeers() []types.RelayDescriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]types.RelayDescriptor, 0, len(r.peers))
	for _, record := range r.peers {
		if record.bootstrap ||
			record.state.State == types.PeerStateExpired ||
			!record.state.Descriptor.SupportsOverlayPeer {
			continue
		}
		out = append(out, record.state.Descriptor)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].APIHTTPSAddr < out[j].APIHTTPSAddr
	})
	return out
}

func (r *peerRegistry) pin(relayID, seedURL string, desc types.RelayDescriptor) error {
	relayID = strings.TrimSpace(relayID)
	if relayID == "" {
		return errors.New("relay id is required")
	}
	normalizedSeedURL, err := utils.NormalizeRelayURL(seedURL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(desc.APIHTTPSAddr) == "" {
		return errors.New("descriptor api_https_addr is required")
	}
	if desc.APIHTTPSAddr != normalizedSeedURL {
		return errors.New("descriptor api_https_addr does not match seed url")
	}
	if strings.TrimSpace(desc.SignerPublicKey) == "" {
		return errors.New("descriptor signer_public_key is required")
	}

	now := time.Now().UTC()

	r.mu.Lock()
	defer r.mu.Unlock()

	record, ok := r.peers[relayID]
	if !ok {
		record = peerRecord{
			state: types.PeerState{
				FirstSeenAt: now,
			},
		}
	}
	if record.seedURL != "" && record.seedURL != normalizedSeedURL {
		return errors.New("seed url does not match cached relay url")
	}
	if record.pinnedSignerPublicKey != "" && record.pinnedSignerPublicKey != desc.SignerPublicKey {
		return errors.New("descriptor signer_public_key does not match pinned signer")
	}

	record.seedURL = normalizedSeedURL
	record.pinnedSignerPublicKey = desc.SignerPublicKey
	r.peers[relayID] = record
	return nil
}

func (r *peerRegistry) register(desc types.RelayDescriptor, advertise bool) (bool, bool, error) {
	if strings.TrimSpace(desc.RelayID) == "" {
		return false, false, errors.New("relay id is required")
	}

	now := time.Now().UTC()

	r.mu.Lock()
	defer r.mu.Unlock()

	record, ok := r.peers[desc.RelayID]
	added := !ok
	if !ok {
		record = peerRecord{
			state: types.PeerState{
				FirstSeenAt: now,
			},
		}
	}

	if strings.TrimSpace(record.seedURL) == "" {
		record.seedURL = strings.TrimSpace(desc.APIHTTPSAddr)
	} else if strings.TrimSpace(desc.APIHTTPSAddr) != "" && record.seedURL != strings.TrimSpace(desc.APIHTTPSAddr) {
		return false, false, errors.New("descriptor api_https_addr does not match cached seed url")
	}
	if record.pinnedSignerPublicKey != "" && record.pinnedSignerPublicKey != desc.SignerPublicKey {
		return false, false, errors.New("descriptor signer_public_key does not match pinned signer")
	}

	previousState := record.state.State
	previousDescriptor := record.state.Descriptor
	switch {
	case advertise:
		record.state.State = types.PeerStateAdvertised
	case record.state.State == types.PeerStateAdvertised:
		record.state.State = types.PeerStateAdvertised
	default:
		record.state.State = types.PeerStateVerified
	}

	record.state.Descriptor = desc
	record.state.LastSeenAt = now
	record.state.ConsecutiveFailures = 0
	r.peers[desc.RelayID] = record

	changed := added ||
		previousState != record.state.State ||
		!reflect.DeepEqual(previousDescriptor, record.state.Descriptor)
	return added, changed, nil
}

func (r *peerRegistry) registerDiscoveredPeers(targetRelayID, targetURL string, selfDescriptor types.RelayDescriptor, peerDescriptors []types.RelayDescriptor) (peerRegistrationResult, error) {
	if strings.TrimSpace(targetRelayID) == "" {
		return peerRegistrationResult{}, errors.New("target relay id is required")
	}
	if err := r.pin(targetRelayID, targetURL, selfDescriptor); err != nil {
		return peerRegistrationResult{}, err
	}

	added, changed, err := r.register(selfDescriptor, true)
	if err != nil {
		return peerRegistrationResult{}, err
	}

	result := peerRegistrationResult{
		updated:        added || changed,
		peerSetChanged: changed,
	}

	for _, peerDescriptor := range peerDescriptors {
		hintAdded, hintChanged, err := r.register(peerDescriptor, false)
		if err != nil {
			return peerRegistrationResult{}, err
		}
		result.peerSetChanged = result.peerSetChanged || hintChanged
		if hintAdded || hintChanged {
			result.updated = true
			result.addedHintCount++
		}
	}

	return result, nil
}

func (r *peerRegistry) fail(relayID string) {
	if strings.TrimSpace(relayID) == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	record, ok := r.peers[relayID]
	if !ok {
		return
	}
	record.state.ConsecutiveFailures++
	r.peers[relayID] = record
}

func (r *peerRegistry) expire(relayID string) bool {
	if strings.TrimSpace(relayID) == "" {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	record, ok := r.peers[relayID]
	if !ok {
		return false
	}
	if record.state.State == types.PeerStateExpired {
		return false
	}
	record.state.State = types.PeerStateExpired
	r.peers[relayID] = record
	return true
}

func (r *peerRegistry) recordFailure(relayID string, err error, recoveryFailures int) peerFailureOutcome {
	r.fail(relayID)

	result := peerFailureOutcome{}
	state, _, ok := r.lookup(relayID)
	if ok &&
		state.State != types.PeerStateExpired &&
		state.ConsecutiveFailures >= recoveryFailures {
		if removed := r.expire(relayID); removed {
			result.expired = true
			result.expireReason = "recovery"
			result.consecutiveFailures = state.ConsecutiveFailures
			return result
		}
	}

	var apiErr *types.APIRequestError
	if errors.As(err, &apiErr) &&
		(apiErr.StatusCode == http.StatusForbidden ||
			apiErr.StatusCode == http.StatusNotFound ||
			apiErr.StatusCode == http.StatusGone) {
		if removed := r.expire(relayID); removed {
			result.expired = true
			result.expireReason = "status"
		}
	}

	return result
}
