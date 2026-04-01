package portal

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/portal/v2/portal/auth"
	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/portal/transport"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

const defaultRegisterChallengeTTL = 2 * time.Minute

type leaseRegistry struct {
	routes             *routeTable
	leasesByKey        map[string]*leaseRecord
	registerChallenges map[string]*auth.RegisterChallenge
	policy             *policy.Runtime
	mu                 sync.RWMutex
}

func newLeaseRegistry(runtime *policy.Runtime) *leaseRegistry {
	if runtime == nil {
		runtime = policy.NewRuntime()
	}

	return &leaseRegistry{
		routes:             newRouteTable(),
		leasesByKey:        make(map[string]*leaseRecord),
		registerChallenges: make(map[string]*auth.RegisterChallenge),
		policy:             runtime,
	}
}

func (r *leaseRegistry) CloseAll() []*leaseRecord {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]*leaseRecord, 0, len(r.leasesByKey))
	for _, record := range r.leasesByKey {
		out = append(out, record)
		r.policy.ForgetIdentity(record.Key())
	}
	r.routes = newRouteTable()
	r.leasesByKey = make(map[string]*leaseRecord)
	r.registerChallenges = make(map[string]*auth.RegisterChallenge)
	return out
}

func (r *leaseRegistry) RunJanitor(ctx context.Context, interval time.Duration, onExpired func(*leaseRecord)) error {
	if interval <= 0 {
		return errors.New("janitor interval must be positive")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.cleanupExpired(time.Now(), onExpired)
		}
	}
}

func (r *leaseRegistry) Lookup(host string) (*leaseRecord, bool) {
	host = utils.NormalizeHostname(host)
	if host == "" {
		return nil, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	key, ok := r.routes.Lookup(host)
	if !ok {
		return nil, false
	}
	record, ok := r.leasesByKey[key]
	return record, ok && record != nil
}

func (r *leaseRegistry) Register(record *leaseRecord) error {
	if record == nil {
		return errors.New("lease record is required")
	}

	key := record.Key()
	if key == "" {
		return errors.New("lease identity is required")
	}
	hostname := utils.NormalizeHostname(record.Hostname)
	if hostname == "" {
		return errors.New("lease hostname is required")
	}

	r.mu.Lock()

	if existingKey, ok := r.routes.LookupExact(hostname); ok && existingKey != key {
		r.mu.Unlock()
		return errHostnameConflict
	}

	var replaced *leaseRecord
	if existing, ok := r.leasesByKey[key]; ok && existing != nil {
		replaced = existing
		r.routes.Delete(existing.Hostname)
		r.policy.ForgetIdentity(existing.Key())
	}
	record.Hostname = hostname
	r.leasesByKey[key] = record
	r.routes.Set(hostname, key)
	if strings.TrimSpace(record.ClientIP) != "" {
		r.policy.IPFilter().RegisterIdentityIP(key, record.ClientIP)
	}
	r.mu.Unlock()

	if replaced != nil && replaced != record {
		replaced.Close()
	}
	return nil
}

func (r *leaseRegistry) Renew(identity types.Identity, ttl time.Duration, clientIP, reportedIP string) (*leaseRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, ok := r.leasesByKey[identity.Key()]
	if !ok {
		return nil, errLeaseNotFound
	}

	now := time.Now()
	record.ExpiresAt = now.Add(ttl)
	record.LastSeenAt = now
	if strings.TrimSpace(clientIP) != "" {
		record.ClientIP = clientIP
		r.policy.IPFilter().RegisterIdentityIP(record.Key(), clientIP)
	}
	if strings.TrimSpace(reportedIP) != "" {
		record.ReportedIP = reportedIP
	}
	return record, nil
}

func (r *leaseRegistry) Unregister(identity types.Identity) (*leaseRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := identity.Key()
	record, ok := r.leasesByKey[key]
	if !ok {
		return nil, errLeaseNotFound
	}

	delete(r.leasesByKey, key)
	r.routes.Delete(record.Hostname)
	r.policy.ForgetIdentity(key)
	return record, nil
}

func (r *leaseRegistry) Find(identity types.Identity) (*leaseRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	record, ok := r.leasesByKey[identity.Key()]
	if !ok || time.Now().After(record.ExpiresAt) {
		return nil, errLeaseNotFound
	}
	return record, nil
}

func (r *leaseRegistry) issueRegisterChallenge(req types.RegisterChallengeRequest, domain, uri string) (types.RegisterChallengeResponse, error) {
	if req.UDPEnabled {
		if !r.policy.IsUDPEnabled() {
			return types.RegisterChallengeResponse{}, errUDPDisabled
		}
		if max := r.policy.UDPMaxLeases(); max > 0 && r.CountDatagramLeases() >= max {
			return types.RegisterChallengeResponse{}, errUDPCapacityExceeded
		}
	}

	now := time.Now().UTC()
	challenge, err := auth.NewRegisterChallenge(req, domain, uri, now, defaultRegisterChallengeTTL)
	if err != nil {
		return types.RegisterChallengeResponse{}, err
	}

	r.mu.Lock()
	r.registerChallenges[challenge.ChallengeID] = challenge
	r.mu.Unlock()

	return types.RegisterChallengeResponse{
		ChallengeID: challenge.ChallengeID,
		ExpiresAt:   challenge.ExpiresAt,
		SIWEMessage: challenge.SIWEMessage,
	}, nil
}

func (r *leaseRegistry) consumeVerifiedRegisterChallenge(req types.RegisterRequest) (*auth.RegisterChallenge, error) {
	challengeID := strings.TrimSpace(req.ChallengeID)
	if challengeID == "" {
		return nil, auth.ErrChallengeNotFound
	}

	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()

	challenge := r.registerChallenges[challengeID]
	if challenge == nil {
		return nil, auth.ErrChallengeNotFound
	}
	if challenge.Expired(now) {
		delete(r.registerChallenges, challengeID)
		return nil, auth.ErrChallengeExpired
	}
	if err := challenge.Verify(req, now); err != nil {
		return nil, err
	}

	delete(r.registerChallenges, challengeID)
	return challenge, nil
}

func (r *leaseRegistry) Touch(identity types.Identity, clientIP string, now time.Time) *leaseRecord {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, ok := r.leasesByKey[identity.Key()]
	if !ok {
		return nil
	}
	record.LastSeenAt = now
	if strings.TrimSpace(clientIP) != "" {
		record.ClientIP = clientIP
		r.policy.IPFilter().RegisterIdentityIP(record.Key(), clientIP)
	}
	return record
}

func (r *leaseRegistry) cleanupExpired(now time.Time, onExpired func(*leaseRecord)) {
	expiredLeases := r.removeExpired(now)
	r.mu.Lock()
	for challengeID, challenge := range r.registerChallenges {
		if challenge == nil || challenge.Expired(now) {
			delete(r.registerChallenges, challengeID)
		}
	}
	r.mu.Unlock()
	for _, lease := range expiredLeases {
		if onExpired != nil {
			onExpired(lease)
		}
		lease.Close()
	}
}

func (r *leaseRegistry) removeExpired(now time.Time) []*leaseRecord {
	r.mu.Lock()
	defer r.mu.Unlock()

	expired := make([]*leaseRecord, 0)
	for key, record := range r.leasesByKey {
		if now.After(record.ExpiresAt) {
			expired = append(expired, record)
			delete(r.leasesByKey, key)
			r.routes.Delete(record.Hostname)
			r.policy.ForgetIdentity(key)
		}
	}
	return expired
}

func (r *leaseRegistry) CountDatagramLeases() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	count := 0
	for _, record := range r.leasesByKey {
		if record.datagram != nil && now.Before(record.ExpiresAt) {
			count++
		}
	}
	return count
}

func (r *leaseRegistry) Snapshot(record *leaseRecord) types.Lease {
	if record == nil {
		return types.Lease{}
	}

	snapshot := types.Lease{
		Name:        record.Name,
		ExpiresAt:   record.ExpiresAt,
		FirstSeenAt: record.FirstSeenAt,
		LastSeenAt:  record.LastSeenAt,
		Hostname:    record.Hostname,
		UDPEnabled:  record.UDPEnabled,
		Metadata:    record.Metadata.Copy(),
	}
	if record.stream != nil {
		snapshot.Ready = record.stream.ReadyCount()
	}
	return snapshot
}

type leaseRecord struct {
	types.Identity
	ExpiresAt   time.Time
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	ClientIP    string
	ReportedIP  string
	Hostname    string
	UDPEnabled  bool
	Metadata    types.LeaseMetadata
	datagram    *transport.RelayDatagram
	ports       *transport.PortAllocator
	stream      *transport.RelayStream
	startErr    error
	startOnce   sync.Once
}

func (r *leaseRegistry) AdminSnapshot(record *leaseRecord) types.AdminLease {
	if record == nil {
		return types.AdminLease{}
	}

	clientIP := record.ClientIP
	identityKey := record.Key()
	return types.AdminLease{
		Lease:      r.Snapshot(record),
		Address:    record.Address,
		BPS:        r.policy.BPSManager().IdentityBPS(identityKey),
		ClientIP:   clientIP,
		ReportedIP: record.ReportedIP,
		IsApproved: r.policy.EffectiveApproval(identityKey),
		IsBanned:   r.policy.IsIdentityBanned(identityKey),
		IsDenied:   r.policy.IsIdentityDenied(identityKey),
		IsIPBanned: r.policy.IPFilter().IsIPBanned(clientIP),
	}
}

func (r *leaseRecord) Start() error {
	if r == nil || r.datagram == nil {
		return nil
	}

	r.startOnce.Do(func() {
		r.startErr = r.datagram.Start(context.Background())
	})
	return r.startErr
}

func (r *leaseRecord) Close() {
	if r == nil {
		return
	}
	if r.stream != nil {
		r.stream.Close()
	}
	if r.datagram != nil {
		port := r.datagram.UDPPort()
		r.datagram.Close()
		if port > 0 && r.ports != nil {
			r.ports.Release(port)
		}
	}
}

type routeTable struct {
	exact map[string]string
}

func newRouteTable() *routeTable {
	return &routeTable{exact: make(map[string]string)}
}

func (t *routeTable) Set(host, identityKey string) {
	host = utils.NormalizeHostname(host)
	if host == "" {
		return
	}
	t.exact[host] = identityKey
}

func (t *routeTable) Delete(host string) {
	delete(t.exact, utils.NormalizeHostname(host))
}

func (t *routeTable) LookupExact(host string) (string, bool) {
	host = utils.NormalizeHostname(host)
	if host == "" {
		return "", false
	}
	identityKey, ok := t.exact[host]
	return identityKey, ok
}

func (t *routeTable) Lookup(host string) (string, bool) {
	host = utils.NormalizeHostname(host)
	if host == "" {
		return "", false
	}

	if identityKey, ok := t.exact[host]; ok {
		return identityKey, true
	}

	parts := strings.Split(host, ".")
	if len(parts) < 3 {
		return "", false
	}
	wildcard := "*." + strings.Join(parts[1:], ".")
	identityKey, ok := t.exact[wildcard]
	return identityKey, ok
}
