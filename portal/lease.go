package portal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gosuda/portal/v2/portal/policy"
	"github.com/gosuda/portal/v2/types"
	"github.com/gosuda/portal/v2/utils"
)

type leaseRegistry struct {
	routes    *routeTable
	leaseByID map[string]*leaseRecord
	policy    *policy.Runtime
	mu        sync.RWMutex
}

func newLeaseRegistry(runtime *policy.Runtime) *leaseRegistry {
	if runtime == nil {
		runtime = policy.NewRuntime()
	}
	return &leaseRegistry{
		routes:    newRouteTable(),
		leaseByID: make(map[string]*leaseRecord),
		policy:    runtime,
	}
}

func (r *leaseRegistry) PolicyRuntime() *policy.Runtime {
	if r == nil {
		return nil
	}
	return r.policy
}

func (r *leaseRegistry) CloseAll() []*leaseRecord {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]*leaseRecord, 0, len(r.leaseByID))
	for _, record := range r.leaseByID {
		out = append(out, record)
		r.policy.ForgetLease(record.ID)
	}
	r.routes = newRouteTable()
	r.leaseByID = make(map[string]*leaseRecord)
	return out
}

func (r *leaseRegistry) RunJanitor(ctx context.Context, interval time.Duration) error {
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
			r.cleanupExpired(time.Now())
		}
	}
}

func (r *leaseRegistry) List() []*leaseRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*leaseRecord, 0, len(r.leaseByID))
	for _, record := range r.leaseByID {
		out = append(out, record)
	}
	return out
}

func (r *leaseRegistry) Get(leaseID string) (*leaseRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	record, ok := r.leaseByID[strings.TrimSpace(leaseID)]
	return record, ok
}

func (r *leaseRegistry) Lookup(host string) (*leaseRecord, bool) {
	host = utils.NormalizeHostname(host)
	if host == "" {
		return nil, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	leaseID, ok := r.routes.Lookup(host)
	if !ok {
		return nil, false
	}
	record, ok := r.leaseByID[leaseID]
	return record, ok && record != nil
}

func (r *leaseRegistry) Register(record *leaseRecord) error {
	if record == nil {
		return errors.New("lease record is required")
	}

	leaseID := strings.TrimSpace(record.ID)
	if leaseID == "" {
		return errors.New("lease id is required")
	}
	hostname := utils.NormalizeHostname(record.Hostname)
	if hostname == "" {
		return errors.New("lease hostname is required")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if ownerLeaseID, ok := r.routes.LookupExact(hostname); ok && ownerLeaseID != leaseID {
		return fmt.Errorf("%w: %s", errHostnameConflict, hostname)
	}

	record.ID = leaseID
	record.Hostname = hostname
	r.leaseByID[leaseID] = record
	r.routes.Set(hostname, leaseID)
	if strings.TrimSpace(record.ClientIP) != "" {
		r.policy.IPFilter().RegisterLeaseIP(leaseID, record.ClientIP)
	}
	return nil
}

func (r *leaseRegistry) Renew(leaseID, reverseToken string, ttl time.Duration, clientIP string) (*leaseRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, ok := r.leaseByID[strings.TrimSpace(leaseID)]
	if !ok {
		return nil, errLeaseNotFound
	}
	if !tokenMatches(record.ReverseToken, reverseToken) {
		return nil, errUnauthorized
	}

	now := time.Now()
	record.ExpiresAt = now.Add(ttl)
	record.LastSeenAt = now
	if strings.TrimSpace(clientIP) != "" {
		record.ClientIP = clientIP
		r.policy.IPFilter().RegisterLeaseIP(record.ID, clientIP)
	}
	return record, nil
}

func (r *leaseRegistry) Unregister(leaseID, reverseToken string) (*leaseRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	record, ok := r.leaseByID[strings.TrimSpace(leaseID)]
	if !ok {
		return nil, errLeaseNotFound
	}
	if !tokenMatches(record.ReverseToken, reverseToken) {
		return nil, errUnauthorized
	}

	delete(r.leaseByID, record.ID)
	r.routes.Delete(record.Hostname)
	r.policy.ForgetLease(record.ID)
	return record, nil
}

func (r *leaseRegistry) FindByID(leaseID string) (*leaseRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	record, ok := r.leaseByID[strings.TrimSpace(leaseID)]
	if !ok || time.Now().After(record.ExpiresAt) {
		return nil, errLeaseNotFound
	}
	return record, nil
}

func (r *leaseRegistry) Touch(leaseID, clientIP string, now time.Time) *leaseRecord {
	r.mu.Lock()
	defer r.mu.Unlock()

	record := r.leaseByID[strings.TrimSpace(leaseID)]
	if record == nil {
		return nil
	}
	record.LastSeenAt = now
	if strings.TrimSpace(clientIP) != "" {
		record.ClientIP = clientIP
		r.policy.IPFilter().RegisterLeaseIP(record.ID, clientIP)
	}
	return record
}

func (r *leaseRegistry) cleanupExpired(now time.Time) {
	for _, lease := range r.removeExpired(now) {
		lease.Broker.Close()
	}
}

func (r *leaseRegistry) removeExpired(now time.Time) []*leaseRecord {
	r.mu.Lock()
	defer r.mu.Unlock()

	expired := make([]*leaseRecord, 0)
	for leaseID, record := range r.leaseByID {
		if now.After(record.ExpiresAt) {
			expired = append(expired, record)
			delete(r.leaseByID, leaseID)
			r.routes.Delete(record.Hostname)
			r.policy.ForgetLease(record.ID)
		}
	}
	return expired
}

func (r *leaseRegistry) IsClientIPBanned(clientIP string) bool {
	return r.policy.IPFilter().IsIPBanned(clientIP)
}

func (r *leaseRegistry) IsRoutable(record *leaseRecord) bool {
	if record == nil {
		return false
	}
	return r.policy.IsLeaseRoutable(record.ID)
}

func (r *leaseRegistry) Snapshot(record *leaseRecord) LeaseSnapshot {
	if record == nil {
		return LeaseSnapshot{}
	}

	clientIP := record.ClientIP
	return LeaseSnapshot{
		ID:          record.ID,
		Name:        record.Name,
		ClientIP:    clientIP,
		Hostname:    record.Hostname,
		Metadata:    record.Metadata,
		ExpiresAt:   record.ExpiresAt,
		FirstSeenAt: record.FirstSeenAt,
		LastSeenAt:  record.LastSeenAt,
		Ready:       record.Broker.ReadyCount(),
		IsApproved:  r.policy.EffectiveApproval(record.ID),
		IsBanned:    r.policy.IsLeaseBanned(record.ID),
		IsDenied:    r.policy.IsLeaseDenied(record.ID),
		IsIPBanned:  r.policy.IPFilter().IsIPBanned(clientIP),
	}
}

type leaseRecord struct {
	ExpiresAt    time.Time
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	Broker       *leaseBroker
	ID           string
	Name         string
	ReverseToken string
	ClientIP     string
	Hostname     string
	Metadata     types.LeaseMetadata
}

type LeaseSnapshot struct {
	ExpiresAt   time.Time
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	ID          string
	Name        string
	ClientIP    string
	Hostname    string
	Metadata    types.LeaseMetadata
	Ready       int
	IsApproved  bool
	IsBanned    bool
	IsDenied    bool
	IsIPBanned  bool
}

type routeTable struct {
	exact map[string]string
}

func newRouteTable() *routeTable {
	return &routeTable{exact: make(map[string]string)}
}

func (t *routeTable) Set(host, leaseID string) {
	host = utils.NormalizeHostname(host)
	if host == "" {
		return
	}
	t.exact[host] = leaseID
}

func (t *routeTable) Delete(host string) {
	delete(t.exact, utils.NormalizeHostname(host))
}

func (t *routeTable) LookupExact(host string) (string, bool) {
	host = utils.NormalizeHostname(host)
	if host == "" {
		return "", false
	}
	leaseID, ok := t.exact[host]
	return leaseID, ok
}

func (t *routeTable) Lookup(host string) (string, bool) {
	host = utils.NormalizeHostname(host)
	if host == "" {
		return "", false
	}

	if leaseID, ok := t.exact[host]; ok {
		return leaseID, true
	}

	parts := strings.Split(host, ".")
	if len(parts) < 3 {
		return "", false
	}
	wildcard := "*." + strings.Join(parts[1:], ".")
	leaseID, ok := t.exact[wildcard]
	return leaseID, ok
}
