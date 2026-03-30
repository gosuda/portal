package discovery

import (
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

type RelayView struct {
	Descriptor  types.RelayDescriptor
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

type RelayLocalState struct {
	Banned              bool
	BanReason           string
	Bootstrap           bool
	Advertised          bool
	Expired             bool
	Reachable           bool
	ConsecutiveFailures int
	LastSuccessAt       time.Time
	LastFailureAt       time.Time
}

type RelaySummary struct {
	Known       int
	Banned      int
	Bootstrap   int
	Advertised  int
	Expired     int
	Syncable    int
	Reachable   int
	Unreachable int
}

// RelaySet owns the shared relay discovery view: known relay URLs, stable relay
// id/url mappings, the latest validated descriptor seen for each relay, and common
// process-local relay state such as ban/reachability/failure tracking.
//
// Runtime-specific policy such as bootstrap classification, relay lifecycle, or
// listener ownership belongs in the caller's projection.
type RelaySet struct {
	mu                  sync.RWMutex
	knownRelayURLs      []string
	relayIDsByURL       map[string]string
	relays              map[string]RelayView
	localByURL          map[string]RelayLocalState
	lastStatusReachable map[string]bool
	lastStatusSummary   RelaySummary
	haveLastStatus      bool
}

func NewRelaySet() *RelaySet {
	return &RelaySet{
		relayIDsByURL: make(map[string]string),
		relays:        make(map[string]RelayView),
		localByURL:    make(map[string]RelayLocalState),
	}
}

func (s *RelaySet) trackedRelayURLs() []string {
	if s == nil {
		return nil
	}

	urls := make([]string, 0, len(s.knownRelayURLs)+len(s.relays))
	seen := make(map[string]struct{}, len(s.knownRelayURLs)+len(s.relays))
	for _, relayURL := range s.knownRelayURLs {
		relayURL = strings.TrimSpace(relayURL)
		if relayURL == "" {
			continue
		}
		if _, ok := seen[relayURL]; ok {
			continue
		}
		seen[relayURL] = struct{}{}
		urls = append(urls, relayURL)
	}
	for _, view := range s.relays {
		relayURL := strings.TrimSpace(view.Descriptor.APIHTTPSAddr)
		if relayURL == "" {
			continue
		}
		if _, ok := seen[relayURL]; ok {
			continue
		}
		seen[relayURL] = struct{}{}
		urls = append(urls, relayURL)
	}
	return urls
}

func (s *RelaySet) ActiveRelayURLs() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.knownRelayURLs) == 0 {
		return nil
	}

	out := make([]string, 0, len(s.knownRelayURLs))
	for _, relayURL := range s.knownRelayURLs {
		if state, ok := s.localByURL[relayURL]; ok && state.Banned {
			continue
		}
		out = append(out, relayURL)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func relayExpiredAt(view RelayView, state RelayLocalState, now time.Time) bool {
	if state.Expired {
		return true
	}
	if view.Descriptor.ExpiresAt.IsZero() {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return !view.Descriptor.ExpiresAt.After(now)
}

func (s *RelaySet) logStatusChange() {
	now := time.Now().UTC()
	var currentReachable map[string]bool
	trackedRelayURLs := s.trackedRelayURLs()
	if len(trackedRelayURLs) > 0 {
		currentReachable = make(map[string]bool, len(trackedRelayURLs))
		for _, relayURL := range trackedRelayURLs {
			state := s.localByURL[relayURL]
			currentReachable[relayURL] = !state.Banned && state.Reachable
		}
	}
	summary := RelaySummary{}
	for _, relayURL := range trackedRelayURLs {
		summary.Known++
		state := s.localByURL[relayURL]
		relayID := s.relayIDsByURL[relayURL]
		view, ok := s.relays[relayID]
		expired := ok && relayExpiredAt(view, state, now) || !ok && state.Expired
		if state.Banned {
			summary.Banned++
			continue
		}
		if state.Bootstrap {
			summary.Bootstrap++
		}
		if state.Advertised && !expired {
			summary.Advertised++
		}
		if expired {
			summary.Expired++
		}
		if state.Reachable {
			summary.Reachable++
		} else {
			summary.Unreachable++
		}
		if ok && !state.Bootstrap && !expired && view.Descriptor.SupportsOverlayPeer {
			summary.Syncable++
		}
	}
	if s.haveLastStatus && summary == s.lastStatusSummary && reflect.DeepEqual(currentReachable, s.lastStatusReachable) {
		return
	}

	activated := make([]string, 0)
	deactivated := make([]string, 0)
	for relayURL, reachable := range currentReachable {
		if s.lastStatusReachable == nil || s.lastStatusReachable[relayURL] == reachable {
			continue
		}
		if reachable {
			activated = append(activated, relayURL)
		} else {
			deactivated = append(deactivated, relayURL)
		}
	}
	for relayURL, reachable := range s.lastStatusReachable {
		if _, ok := currentReachable[relayURL]; ok || !reachable {
			continue
		}
		deactivated = append(deactivated, relayURL)
	}

	event := log.Info().
		Int("banned", summary.Banned).
		Int("bootstrap", summary.Bootstrap).
		Int("advertised", summary.Advertised).
		Int("expired", summary.Expired).
		Int("syncable", summary.Syncable).
		Int("reachable", summary.Reachable).
		Int("unreachable", summary.Unreachable)
	if len(activated) > 0 {
		event = event.Strs("activated", activated)
	}
	if len(deactivated) > 0 {
		event = event.Strs("deactivated", deactivated)
	}
	event.Msg("relay status")
	s.lastStatusReachable = currentReachable
	s.lastStatusSummary = summary
	s.haveLastStatus = true
}

func (s *RelaySet) BootstrapDescriptors() []types.RelayDescriptor {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.knownRelayURLs) == 0 {
		return nil
	}

	out := make([]types.RelayDescriptor, 0, len(s.knownRelayURLs))
	for _, relayURL := range s.knownRelayURLs {
		state, ok := s.localByURL[relayURL]
		if !ok || !state.Bootstrap {
			continue
		}
		if relayID, ok := s.relayIDsByURL[relayURL]; ok {
			if view, ok := s.relays[relayID]; ok && strings.TrimSpace(view.Descriptor.APIHTTPSAddr) != "" {
				out = append(out, view.Descriptor)
				continue
			}
		}
		out = append(out, types.RelayDescriptor{
			RelayID:      relayURL,
			APIHTTPSAddr: relayURL,
			Version:      1,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *RelaySet) BanRelayURL(relayURL, reason string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	relayURL = strings.TrimSpace(relayURL)
	if relayURL == "" {
		return false
	}

	state := s.localByURL[relayURL]
	changed := !state.Banned || strings.TrimSpace(state.BanReason) != strings.TrimSpace(reason)
	state.Banned = true
	state.BanReason = strings.TrimSpace(reason)
	state.Reachable = false
	s.localByURL[relayURL] = state
	if changed {
		s.logStatusChange()
	}
	return changed
}

func (s *RelaySet) MarkRelayUnreachable(relayURL string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	relayURL = strings.TrimSpace(relayURL)
	if relayURL == "" {
		return false
	}

	state := s.localByURL[relayURL]
	if state.Banned {
		return false
	}
	if !state.Reachable {
		return false
	}
	state.Reachable = false
	s.localByURL[relayURL] = state
	s.logStatusChange()
	return true
}

func (s *RelaySet) MarkRelayReachable(relayURL string, now time.Time) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	relayURL = strings.TrimSpace(relayURL)
	if relayURL == "" {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	state := s.localByURL[relayURL]
	changed := !state.Reachable || state.ConsecutiveFailures != 0 || state.LastSuccessAt != now
	state.Reachable = true
	state.ConsecutiveFailures = 0
	state.LastSuccessAt = now
	s.localByURL[relayURL] = state
	if changed {
		s.logStatusChange()
	}
	return changed
}

func (s *RelaySet) MarkRelayFailure(relayURL string, now time.Time) RelayLocalState {
	if s == nil {
		return RelayLocalState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	relayURL = strings.TrimSpace(relayURL)
	if relayURL == "" {
		return RelayLocalState{}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	state := s.localByURL[relayURL]
	state.Reachable = false
	state.ConsecutiveFailures++
	state.LastFailureAt = now
	s.localByURL[relayURL] = state
	s.logStatusChange()
	return state
}

func (s *RelaySet) AdvertisedDescriptors() []types.RelayDescriptor {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.relays) == 0 {
		return nil
	}

	now := time.Now().UTC()
	out := make([]types.RelayDescriptor, 0, len(s.relays))
	for _, view := range s.relays {
		state := s.localByURL[view.Descriptor.APIHTTPSAddr]
		if !state.Advertised || relayExpiredAt(view, state, now) || strings.TrimSpace(view.Descriptor.APIHTTPSAddr) == "" {
			continue
		}
		out = append(out, view.Descriptor)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].APIHTTPSAddr < out[j].APIHTTPSAddr
	})
	return out
}

func (s *RelaySet) SyncableDescriptors() []types.RelayDescriptor {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.relays) == 0 {
		return nil
	}

	now := time.Now().UTC()
	out := make([]types.RelayDescriptor, 0, len(s.relays))
	for _, view := range s.relays {
		state := s.localByURL[view.Descriptor.APIHTTPSAddr]
		if state.Bootstrap || relayExpiredAt(view, state, now) || !view.Descriptor.SupportsOverlayPeer {
			continue
		}
		out = append(out, view.Descriptor)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].APIHTTPSAddr < out[j].APIHTTPSAddr
	})
	return out
}

func (s *RelaySet) Snapshot() map[string]types.RelayState {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.relays) == 0 {
		return nil
	}

	now := time.Now().UTC()
	snapshot := make(map[string]types.RelayState, len(s.relays))
	for relayID, view := range s.relays {
		localState := s.localByURL[view.Descriptor.APIHTTPSAddr]
		snapshot[relayID] = types.RelayState{
			Descriptor:          view.Descriptor,
			Bootstrap:           localState.Bootstrap,
			Advertised:          localState.Advertised,
			Expired:             relayExpiredAt(view, localState, now),
			FirstSeenAt:         view.FirstSeenAt,
			LastSeenAt:          view.LastSeenAt,
			ConsecutiveFailures: localState.ConsecutiveFailures,
		}
	}
	return snapshot
}

func (s *RelaySet) ReplaceKnownRelayURLs(relayURLs []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	filtered := make([]string, 0, len(relayURLs))
	for _, relayURL := range relayURLs {
		relayURL = strings.TrimSpace(relayURL)
		if relayURL == "" {
			continue
		}
		duplicate := false
		for _, existing := range filtered {
			if existing == relayURL {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		filtered = append(filtered, relayURL)
	}
	s.knownRelayURLs = append([]string(nil), filtered...)
}

func (s *RelaySet) registerDescriptor(desc types.RelayDescriptor, now time.Time) (string, bool, bool, error) {
	if s == nil {
		return "", false, false, nil
	}
	normalized, err := NormalizeDescriptor(desc)
	if err != nil {
		return "", false, false, err
	}
	if current, ok := s.relays[normalized.RelayID]; ok {
		currentURL := strings.TrimSpace(current.Descriptor.APIHTTPSAddr)
		if currentURL != "" && currentURL != normalized.APIHTTPSAddr {
			return "", false, false, errors.New("descriptor api_https_addr does not match known relay url")
		}
	}
	if knownRelayID, ok := s.relayIDsByURL[normalized.APIHTTPSAddr]; ok && knownRelayID != normalized.RelayID {
		return "", false, false, errors.New("descriptor relay_id does not match known relay")
	}

	if now.IsZero() {
		now = time.Now().UTC()
	}

	relayID := normalized.RelayID
	view, ok := s.relays[relayID]
	added := !ok
	if !ok {
		view.FirstSeenAt = now
	}
	previousDescriptor := view.Descriptor
	view.Descriptor = normalized
	view.LastSeenAt = now
	s.relays[relayID] = view
	s.relayIDsByURL[normalized.APIHTTPSAddr] = relayID

	changed := added || !reflect.DeepEqual(previousDescriptor, normalized)
	return relayID, added, changed, nil
}

func relayDiscoveryURLs(selfDescriptor types.RelayDescriptor, relayDescriptors []types.RelayDescriptor) []string {
	relayURLs := make([]string, 0, 1+len(relayDescriptors))
	if apiURL := strings.TrimSpace(selfDescriptor.APIHTTPSAddr); apiURL != "" {
		relayURLs = append(relayURLs, apiURL)
	}
	for _, relayDescriptor := range relayDescriptors {
		if apiURL := strings.TrimSpace(relayDescriptor.APIHTTPSAddr); apiURL != "" {
			relayURLs = append(relayURLs, apiURL)
		}
	}
	if len(relayURLs) == 0 {
		return nil
	}
	return relayURLs
}

func (s *RelaySet) applyDiscoveryDescriptors(targetRelayID, targetURL string, selfDescriptor types.RelayDescriptor, relayDescriptors []types.RelayDescriptor, now time.Time) (relaySetChanged bool, addedRelayCount int, err error) {
	if s == nil {
		return false, 0, nil
	}
	if strings.TrimSpace(targetRelayID) == "" {
		return false, 0, errors.New("target relay id is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := ValidateDescriptorTarget(selfDescriptor, targetRelayID, targetURL); err != nil {
		return false, 0, err
	}

	apply := func(desc types.RelayDescriptor, advertise, countAdded bool) error {
		_, added, descriptorChanged, err := s.registerDescriptor(desc, now)
		if err != nil {
			return err
		}
		localState := s.localByURL[desc.APIHTTPSAddr]
		wasAdvertised := localState.Advertised
		wasExpired := localState.Expired
		if advertise {
			localState.Advertised = true
		}
		localState.Expired = false
		s.localByURL[desc.APIHTTPSAddr] = localState

		changed := added || descriptorChanged || advertise && !wasAdvertised || wasExpired
		if added && countAdded {
			addedRelayCount++
		}
		if changed {
			relaySetChanged = true
		}
		return nil
	}

	if err := apply(selfDescriptor, true, false); err != nil {
		return false, 0, err
	}
	for _, relayDescriptor := range relayDescriptors {
		if err := apply(relayDescriptor, false, true); err != nil {
			return false, 0, err
		}
	}
	state := s.localByURL[selfDescriptor.APIHTTPSAddr]
	state.Reachable = true
	state.ConsecutiveFailures = 0
	state.LastSuccessAt = now
	s.localByURL[selfDescriptor.APIHTTPSAddr] = state
	s.logStatusChange()
	return relaySetChanged, addedRelayCount, nil
}

func (s *RelaySet) ApplyRelayDiscoveryResponse(targetRelayID, targetURL string, resp types.DiscoveryResponse, now time.Time) (relayURLs []string, relaySetChanged bool, addedRelayCount int, warnErr error, err error) {
	selfDescriptor, relayDescriptors, validateErr := ValidateRelayDiscoveryResponse(resp, now)
	warnErr = validateErr
	if selfDescriptor.RelayID == "" {
		return nil, false, 0, warnErr, validateErr
	}
	if selfDescriptor.RelayID != strings.TrimSpace(targetRelayID) {
		return nil, false, 0, warnErr, errors.New("relay discovery response relay_id mismatch")
	}
	s.mu.Lock()
	relaySetChanged, addedRelayCount, err = s.applyDiscoveryDescriptors(targetRelayID, targetURL, selfDescriptor, relayDescriptors, now)
	s.mu.Unlock()
	if err != nil {
		return nil, false, 0, warnErr, err
	}
	return relayDiscoveryURLs(selfDescriptor, relayDescriptors), relaySetChanged, addedRelayCount, warnErr, nil
}

func (s *RelaySet) ApplyOverlayRelayDiscoveryResponse(targetRelayID, targetURL string, resp types.DiscoveryResponse, now time.Time) (relayURLs []string, relaySetChanged bool, addedRelayCount int, warnErr error, err error) {
	selfDescriptor, relayDescriptors, validateErr := ValidateRelayDiscoveryResponse(resp, now)
	warnErr = validateErr
	if selfDescriptor.RelayID == "" {
		return nil, false, 0, warnErr, validateErr
	}
	if selfDescriptor.RelayID != strings.TrimSpace(targetRelayID) {
		return nil, false, 0, warnErr, errors.New("relay discovery response relay_id mismatch")
	}
	if err := RequireOverlayRelayDescriptor(selfDescriptor); err != nil {
		return nil, false, 0, warnErr, err
	}

	filteredRelayDescriptors := make([]types.RelayDescriptor, 0, len(relayDescriptors))
	for _, relayDescriptor := range relayDescriptors {
		if err := RequireOverlayRelayDescriptor(relayDescriptor); err != nil {
			if warnErr == nil {
				warnErr = err
			}
			continue
		}
		filteredRelayDescriptors = append(filteredRelayDescriptors, relayDescriptor)
	}

	s.mu.Lock()
	relaySetChanged, addedRelayCount, err = s.applyDiscoveryDescriptors(targetRelayID, targetURL, selfDescriptor, filteredRelayDescriptors, now)
	s.mu.Unlock()
	if err != nil {
		return nil, false, 0, warnErr, err
	}
	return relayDiscoveryURLs(selfDescriptor, filteredRelayDescriptors), relaySetChanged, addedRelayCount, warnErr, nil
}

func (s *RelaySet) RegisterBootstrapRelayURLs(inputs []string, now time.Time) ([]string, error) {
	if s == nil || len(inputs) == 0 {
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
	if len(normalized) == 0 {
		return nil, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing := make(map[string]struct{}, len(s.knownRelayURLs))
	for _, relayURL := range s.knownRelayURLs {
		existing[relayURL] = struct{}{}
	}
	added := make([]string, 0, len(normalized))
	for _, relayURL := range normalized {
		if _, ok := existing[relayURL]; ok {
			continue
		}
		existing[relayURL] = struct{}{}
		s.knownRelayURLs = append(s.knownRelayURLs, relayURL)
		added = append(added, relayURL)
	}
	for _, relayURL := range normalized {
		state := s.localByURL[relayURL]
		state.Bootstrap = true
		state.Reachable = false
		s.localByURL[relayURL] = state
		if descriptor, err := SeedDescriptor(relayURL); err == nil {
			_, _, _, _ = s.registerDescriptor(descriptor, now)
		}
	}
	s.logStatusChange()
	if len(added) == 0 {
		return nil, nil
	}
	return added, nil
}

func (s *RelaySet) RecordDiscoveryFailure(relayID, relayURL string, err error, recoveryFailures int, now time.Time) (expired bool, expireReason string, consecutiveFailures int) {
	if s == nil {
		return false, "", 0
	}
	relayID = strings.TrimSpace(relayID)
	if relayID == "" {
		return false, "", 0
	}
	relayURL = strings.TrimSpace(relayURL)
	if relayURL == "" {
		return false, "", 0
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	view, ok := s.relays[relayID]
	if !ok {
		return false, "", 0
	}

	localState := s.localByURL[relayURL]
	localState.Reachable = false
	localState.ConsecutiveFailures++
	localState.LastFailureAt = now
	s.localByURL[relayURL] = localState
	s.logStatusChange()
	if !localState.Expired && localState.ConsecutiveFailures >= recoveryFailures {
		state := s.localByURL[view.Descriptor.APIHTTPSAddr]
		state.Expired = true
		s.localByURL[view.Descriptor.APIHTTPSAddr] = state
		s.logStatusChange()
		return true, "recovery", localState.ConsecutiveFailures
	}

	var apiErr *types.APIRequestError
	if errors.As(err, &apiErr) &&
		(apiErr.StatusCode == http.StatusForbidden ||
			apiErr.StatusCode == http.StatusNotFound ||
			apiErr.StatusCode == http.StatusGone) {
		state := s.localByURL[view.Descriptor.APIHTTPSAddr]
		state.Expired = true
		s.localByURL[view.Descriptor.APIHTTPSAddr] = state
		s.logStatusChange()
		return true, "status", localState.ConsecutiveFailures
	}
	return false, "", localState.ConsecutiveFailures
}
