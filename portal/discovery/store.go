package discovery

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

type peerRecord struct {
	seedURL               string
	pinnedSignerPublicKey string
	state                 types.PeerState
}

// Cache keeps only verified relay descriptors and bootstrap hints.
// It is not a source of truth; trust comes from seed URLs plus signer pinning.
type Cache struct {
	mu    sync.RWMutex
	peers map[string]peerRecord
}

func (s *Cache) Lookup(relayID string) (types.PeerState, bool) {
	if strings.TrimSpace(relayID) == "" {
		return types.PeerState{}, false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	record, ok := s.peers[relayID]
	if !ok {
		return types.PeerState{}, false
	}
	return record.state, true
}

func NewCache() *Cache {
	return &Cache{
		peers: make(map[string]peerRecord),
	}
}

func SeedDescriptor(apiURL string) (types.RelayDescriptor, error) {
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

func (s *Cache) UpsertSeedURLs(inputs []string) ([]string, error) {
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

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, apiURL := range normalized {
		descriptor, err := SeedDescriptor(apiURL)
		if err != nil {
			return nil, err
		}

		record, ok := s.peers[descriptor.RelayID]
		if !ok {
			s.peers[descriptor.RelayID] = peerRecord{
				seedURL: descriptor.APIHTTPSAddr,
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
		s.peers[descriptor.RelayID] = record
	}

	return added, nil
}

func (s *Cache) Snapshot() map[string]types.PeerState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]types.PeerState, len(s.peers))
	for relayID, record := range s.peers {
		out[relayID] = record.state
	}
	return out
}

func (s *Cache) SeedURL(relayID string) string {
	if strings.TrimSpace(relayID) == "" {
		return ""
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	record, ok := s.peers[relayID]
	if !ok {
		return ""
	}
	return record.seedURL
}

func (s *Cache) KnownDescriptors() []types.RelayDescriptor {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]types.RelayDescriptor, 0, len(s.peers))
	for _, record := range s.peers {
		if strings.TrimSpace(record.state.Descriptor.APIHTTPSAddr) == "" {
			continue
		}
		out = append(out, record.state.Descriptor)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].APIHTTPSAddr < out[j].APIHTTPSAddr
	})
	return out
}

func (s *Cache) AdvertisedDescriptors() []types.RelayDescriptor {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]types.RelayDescriptor, 0, len(s.peers))
	for _, record := range s.peers {
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

func (s *Cache) HasPinnedIdentity(relayID string) bool {
	if strings.TrimSpace(relayID) == "" {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	record, ok := s.peers[relayID]
	return ok && strings.TrimSpace(record.pinnedSignerPublicKey) != ""
}

func (s *Cache) PinIdentity(relayID, seedURL string, desc types.RelayDescriptor) error {
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

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.peers[relayID]
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
	s.peers[relayID] = record
	return nil
}

func (s *Cache) RecordVerified(desc types.RelayDescriptor, advertise bool) (bool, bool, error) {
	if strings.TrimSpace(desc.RelayID) == "" {
		return false, false, errors.New("relay id is required")
	}

	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.peers[desc.RelayID]
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
	s.peers[desc.RelayID] = record

	changed := added ||
		previousState != record.state.State ||
		!reflect.DeepEqual(previousDescriptor, record.state.Descriptor)
	return added, changed, nil
}

func (s *Cache) RecordFailure(relayID string) {
	if strings.TrimSpace(relayID) == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.peers[relayID]
	if !ok {
		return
	}
	record.state.ConsecutiveFailures++
	s.peers[relayID] = record
}

func (s *Cache) Expire(relayID string) bool {
	if strings.TrimSpace(relayID) == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.peers[relayID]
	if !ok {
		return false
	}
	if record.state.State == types.PeerStateExpired {
		return false
	}
	record.state.State = types.PeerStateExpired
	s.peers[relayID] = record
	return true
}
