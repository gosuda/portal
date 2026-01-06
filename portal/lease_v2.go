package portal

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"regexp"
	"sync"
	"time"

	"gosuda.org/portal/portal/corev2/common"
	"gosuda.org/portal/portal/corev2/identity"
)

var _base32Encoding = base32.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567").WithPadding(base32.NoPadding)

// LeaseV2 represents a lease in V2 protocol (no protobuf)
type LeaseV2 struct {
	SessionID    common.SessionID
	Name         string
	ALPN         []string
	Metadata     string
	PublicKey    []byte
	Signature    common.Signature
	Expires      time.Time
	LastSeen     time.Time
	FirstSeen    time.Time
	ConnectionID int64
}

// ParsedMetadataV2 holds struct-parsed metadata for V2
type ParsedMetadataV2 struct {
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Thumbnail   string   `json:"thumbnail"`
	Owner       string   `json:"owner"`
	Hide        bool     `json:"hide"`
}

// LeaseV2Entry wraps lease with parsed metadata
type LeaseV2Entry struct {
	Lease          *LeaseV2
	ParsedMetadata *ParsedMetadataV2
}

// LeaseRegisterRequest is sent to register a lease (V2)
type LeaseRegisterRequest struct {
	SessionID common.SessionID
	Name      string
	ALPN      []string
	Metadata  string
	PublicKey []byte
	Expires   int64 // Unix timestamp
	Timestamp int64 // Unix timestamp
	Nonce     []byte
}

// LeaseRegisterResponse is response (V2)
type LeaseRegisterResponse struct {
	Status    uint8
	SessionID common.SessionID
	Expires   int64 // Unix timestamp
}

// LeaseRefreshRequest is sent to refresh a lease (V2)
type LeaseRefreshRequest struct {
	SessionID common.SessionID
	PublicKey []byte
	Expires   int64 // Unix timestamp
	Timestamp int64 // Unix timestamp
	Nonce     []byte
	Signature common.Signature
}

// LeaseRefreshResponse is response (V2)
type LeaseRefreshResponse struct {
	Status  uint8
	Expires int64 // Unix timestamp
}

// LeaseDeleteRequest is sent to delete a lease (V2)
type LeaseDeleteRequest struct {
	SessionID common.SessionID
	PublicKey []byte
	Timestamp int64
	Nonce     []byte
	Signature common.Signature
}

// LeaseDeleteResponse is response (V2)
type LeaseDeleteResponse struct {
	Status uint8
}

// LeaseManagerV2 manages V2 leases
type LeaseManagerV2 struct {
	leases      map[common.SessionID]*LeaseV2Entry
	leasesLock  sync.RWMutex
	stopCh      chan struct{}
	ttlInterval time.Duration

	// Policy controls
	bannedLeases map[common.SessionID]struct{}
	namePattern  *regexp.Regexp
	minTTL       time.Duration
	maxTTL       time.Duration
	nameIndex    map[string]common.SessionID // Name -> SessionID mapping
}

// NewLeaseManagerV2 creates a new V2 lease manager
func NewLeaseManagerV2(ttlInterval time.Duration) *LeaseManagerV2 {
	return &LeaseManagerV2{
		leases:       make(map[common.SessionID]*LeaseV2Entry),
		stopCh:       make(chan struct{}),
		ttlInterval:  ttlInterval,
		bannedLeases: make(map[common.SessionID]struct{}),
		nameIndex:    make(map[string]common.SessionID),
	}
}

// Start starts TTL worker
func (lm *LeaseManagerV2) Start() {
	go lm.ttlWorker()
}

// Stop stops the TTL worker
func (lm *LeaseManagerV2) Stop() {
	close(lm.stopCh)
}

// ttlWorker periodically cleans up expired leases
func (lm *LeaseManagerV2) ttlWorker() {
	ticker := time.NewTicker(lm.ttlInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			lm.cleanupExpiredLeases()
		case <-lm.stopCh:
			return
		}
	}
}

// cleanupExpiredLeases removes expired leases
func (lm *LeaseManagerV2) cleanupExpiredLeases() {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	now := time.Now()
	for sessionID, entry := range lm.leases {
		if now.After(entry.Lease.Expires) {
			delete(lm.leases, sessionID)
			if entry.Lease.Name != "" {
				delete(lm.nameIndex, entry.Lease.Name)
			}
		}
	}
}

// sessionIDFromString converts a base32 encoded ID string to SessionID
func sessionIDFromString(id string) (common.SessionID, error) {
	var sessionID common.SessionID
	decoded, err := _base32Encoding.DecodeString(id)
	if err != nil {
		return sessionID, err
	}
	if len(decoded) != common.SessionIDSize {
		return sessionID, common.ErrInvalidSessionID
	}
	copy(sessionID[:], decoded[:common.SessionIDSize])
	return sessionID, nil
}

// RegisterLease registers a new V2 lease
func (lm *LeaseManagerV2) RegisterLease(req *LeaseRegisterRequest, connectionID int64) (*LeaseRegisterResponse, error) {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	// Check if lease is already expired
	expires := time.Unix(req.Expires, 0)
	if time.Now().After(expires) {
		return &LeaseRegisterResponse{
			Status: common.StatusInvalidArgument,
		}, nil
	}

	// Check if banned
	if _, banned := lm.bannedLeases[req.SessionID]; banned {
		return &LeaseRegisterResponse{
			Status: common.StatusUnauthorized,
		}, nil
	}

	// Name pattern check
	if lm.namePattern != nil && req.Name != "" && !lm.namePattern.MatchString(req.Name) {
		return &LeaseRegisterResponse{
			Status: common.StatusInvalidArgument,
		}, nil
	}

	// TTL bounds check
	if lm.minTTL > 0 || lm.maxTTL > 0 {
		ttl := time.Until(expires)
		if lm.minTTL > 0 && ttl < lm.minTTL {
			return &LeaseRegisterResponse{
				Status: common.StatusInvalidArgument,
			}, nil
		}
		if lm.maxTTL > 0 && ttl > lm.maxTTL {
			return &LeaseRegisterResponse{
				Status: common.StatusInvalidArgument,
			}, nil
		}
	}

	// Name conflict check (only if name is not empty)
	if req.Name != "" && req.Name != "(unnamed)" {
		if existingSessionID, exists := lm.nameIndex[req.Name]; exists {
			// Check if it's the same session (updating)
			if !bytes.Equal(existingSessionID[:], req.SessionID[:]) {
				// Different session using same name - conflict
				return &LeaseRegisterResponse{
					Status: common.StatusConflict,
				}, nil
			}
		}
	}

	// Deserialize and verify certificate
	cert, err := identity.DeserializeCertificateV2(req.PublicKey)
	if err != nil {
		return &LeaseRegisterResponse{
			Status: common.StatusUnauthorized,
		}, nil
	}

	// Verify certificate signature
	if err := identity.VerifyCert(*cert); err != nil {
		return &LeaseRegisterResponse{
			Status: common.StatusUnauthorized,
		}, nil
	}

	// Derive session ID from certificate and compare
	derivedSessionID, err := sessionIDFromString(cert.ID)
	if err != nil {
		return &LeaseRegisterResponse{
			Status: common.StatusUnauthorized,
		}, nil
	}

	if !bytes.Equal(derivedSessionID[:], req.SessionID[:]) {
		return &LeaseRegisterResponse{
			Status: common.StatusUnauthorized,
		}, nil
	}

	// Parse metadata
	var parsedMeta *ParsedMetadataV2
	if req.Metadata != "" {
		var meta struct {
			Description string   `json:"description"`
			Tags        []string `json:"tags"`
			Thumbnail   string   `json:"thumbnail"`
			Owner       string   `json:"owner"`
			Hide        bool     `json:"hide"`
		}
		if err := json.Unmarshal([]byte(req.Metadata), &meta); err == nil {
			parsedMeta = &ParsedMetadataV2{
				Description: meta.Description,
				Tags:        meta.Tags,
				Thumbnail:   meta.Thumbnail,
				Owner:       meta.Owner,
				Hide:        meta.Hide,
			}
		}
	}

	// Determine first seen time
	var firstSeen time.Time
	if existing, exists := lm.leases[req.SessionID]; exists {
		firstSeen = existing.Lease.FirstSeen
	}
	if firstSeen.IsZero() {
		firstSeen = time.Now()
	}

	// Create/update lease
	lease := &LeaseV2{
		SessionID:    req.SessionID,
		Name:         req.Name,
		ALPN:         req.ALPN,
		Metadata:     req.Metadata,
		PublicKey:    req.PublicKey,
		Expires:      expires,
		LastSeen:     time.Now(),
		FirstSeen:    firstSeen,
		ConnectionID: connectionID,
	}

	// Update name index
	if req.Name != "" {
		lm.nameIndex[req.Name] = req.SessionID
	}

	lm.leases[req.SessionID] = &LeaseV2Entry{
		Lease:          lease,
		ParsedMetadata: parsedMeta,
	}

	return &LeaseRegisterResponse{
		Status:    common.StatusOK,
		SessionID: req.SessionID,
		Expires:   req.Expires,
	}, nil
}

// RefreshLease refreshes an existing V2 lease
func (lm *LeaseManagerV2) RefreshLease(req *LeaseRefreshRequest, connectionID int64) (*LeaseRefreshResponse, error) {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	entry, exists := lm.leases[req.SessionID]
	if !exists {
		return &LeaseRefreshResponse{
			Status: common.StatusNotFound,
		}, nil
	}

	// Check if banned
	if _, banned := lm.bannedLeases[req.SessionID]; banned {
		return &LeaseRefreshResponse{
			Status: common.StatusUnauthorized,
		}, nil
	}

	// Verify certificate matches
	if !bytes.Equal(entry.Lease.PublicKey, req.PublicKey) {
		return &LeaseRefreshResponse{
			Status: common.StatusUnauthorized,
		}, nil
	}

	// Verify signature (placeholder - implement actual verification)
	// TODO: Implement signature verification using identity.Credential.Verify()

	// Check if new expiry is valid
	expires := time.Unix(req.Expires, 0)
	if time.Now().After(expires) {
		return &LeaseRefreshResponse{
			Status: common.StatusInvalidArgument,
		}, nil
	}

	// TTL bounds check
	if lm.minTTL > 0 || lm.maxTTL > 0 {
		ttl := time.Until(expires)
		if lm.minTTL > 0 && ttl < lm.minTTL {
			return &LeaseRefreshResponse{
				Status: common.StatusInvalidArgument,
			}, nil
		}
		if lm.maxTTL > 0 && ttl > lm.maxTTL {
			return &LeaseRefreshResponse{
				Status: common.StatusInvalidArgument,
			}, nil
		}
	}

	// Update lease
	entry.Lease.Expires = expires
	entry.Lease.LastSeen = time.Now()
	entry.Lease.ConnectionID = connectionID

	return &LeaseRefreshResponse{
		Status:  common.StatusOK,
		Expires: req.Expires,
	}, nil
}

// DeleteLease deletes a V2 lease
func (lm *LeaseManagerV2) DeleteLease(req *LeaseDeleteRequest) (*LeaseDeleteResponse, error) {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	entry, exists := lm.leases[req.SessionID]
	if !exists {
		return &LeaseDeleteResponse{
			Status: common.StatusNotFound,
		}, nil
	}

	// Verify certificate matches
	if !bytes.Equal(entry.Lease.PublicKey, req.PublicKey) {
		return &LeaseDeleteResponse{
			Status: common.StatusUnauthorized,
		}, nil
	}

	// Verify signature (placeholder)
	// TODO: Implement signature verification

	// Remove lease
	delete(lm.leases, req.SessionID)
	if entry.Lease.Name != "" {
		delete(lm.nameIndex, entry.Lease.Name)
	}

	return &LeaseDeleteResponse{
		Status: common.StatusOK,
	}, nil
}

// GetLease retrieves a lease by session ID
func (lm *LeaseManagerV2) GetLease(sessionID common.SessionID) (*LeaseV2Entry, bool) {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()

	entry, exists := lm.leases[sessionID]
	if !exists {
		return nil, false
	}

	// Check if expired
	if time.Now().After(entry.Lease.Expires) {
		return nil, false
	}

	return entry, true
}

// GetLeaseByName retrieves a lease by name
func (lm *LeaseManagerV2) GetLeaseByName(name string) (*LeaseV2Entry, bool) {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()

	if name == "" {
		return nil, false
	}

	sessionID, exists := lm.nameIndex[name]
	if !exists {
		return nil, false
	}

	entry, exists := lm.leases[sessionID]
	if !exists {
		return nil, false
	}

	// Check if banned
	if _, banned := lm.bannedLeases[sessionID]; banned {
		return nil, false
	}

	// Check if expired
	if time.Now().After(entry.Lease.Expires) {
		return nil, false
	}

	return entry, true
}

// GetAllLeases returns all valid leases
func (lm *LeaseManagerV2) GetAllLeases() []*LeaseV2 {
	lm.leasesLock.RLock()
	defer lm.leasesLock.RUnlock()

	now := time.Now()
	var validLeases []*LeaseV2

	for _, entry := range lm.leases {
		if now.Before(entry.Lease.Expires) {
			validLeases = append(validLeases, entry.Lease)
		}
	}

	return validLeases
}

// BanLease bans a lease
func (lm *LeaseManagerV2) BanLease(sessionID common.SessionID) {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	lm.bannedLeases[sessionID] = struct{}{}
}

// UnbanLease unbans a lease
func (lm *LeaseManagerV2) UnbanLease(sessionID common.SessionID) {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	delete(lm.bannedLeases, sessionID)
}

// SetNamePattern sets the name pattern policy
func (lm *LeaseManagerV2) SetNamePattern(pattern string) error {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	if pattern == "" {
		lm.namePattern = nil
		return nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	lm.namePattern = re
	return nil
}

// SetTTLBounds sets the TTL bounds
func (lm *LeaseManagerV2) SetTTLBounds(min, max time.Duration) {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	lm.minTTL = min
	lm.maxTTL = max
}

// CleanupLeasesByConnectionID removes leases for a specific connection
func (lm *LeaseManagerV2) CleanupLeasesByConnectionID(connectionID int64) []common.SessionID {
	lm.leasesLock.Lock()
	defer lm.leasesLock.Unlock()

	var cleanedSessionIDs []common.SessionID
	for sessionID, entry := range lm.leases {
		if entry.Lease.ConnectionID == connectionID {
			delete(lm.leases, sessionID)
			if entry.Lease.Name != "" {
				delete(lm.nameIndex, entry.Lease.Name)
			}
			cleanedSessionIDs = append(cleanedSessionIDs, sessionID)
		}
	}

	return cleanedSessionIDs
}

// SerializeLeaseRegisterRequest serializes a lease register request to bytes
func SerializeLeaseRegisterRequest(req *LeaseRegisterRequest) ([]byte, error) {
	// Format:
	// [SessionID:16][NameLen:2][Name:var][ALPNCount:2][ALPNLen:2][ALPN:var][MetadataLen:2][Metadata:var][PubKeyLen:2][PubKey:var][Expires:8][Timestamp:8][NonceLen:2][Nonce:var]

	buf := new(bytes.Buffer)

	// Session ID
	buf.Write(req.SessionID[:])

	// Name
	nameLen := uint16(len(req.Name))
	binary.Write(buf, binary.BigEndian, nameLen)
	buf.WriteString(req.Name)

	// ALPN
	alpnCount := uint16(len(req.ALPN))
	binary.Write(buf, binary.BigEndian, alpnCount)
	for _, alpn := range req.ALPN {
		alpnLen := uint16(len(alpn))
		binary.Write(buf, binary.BigEndian, alpnLen)
		buf.WriteString(alpn)
	}

	// Metadata
	metadataLen := uint16(len(req.Metadata))
	binary.Write(buf, binary.BigEndian, metadataLen)
	buf.WriteString(req.Metadata)

	// Public Key
	pubKeyLen := uint16(len(req.PublicKey))
	binary.Write(buf, binary.BigEndian, pubKeyLen)
	buf.Write(req.PublicKey)

	// Expires
	binary.Write(buf, binary.BigEndian, req.Expires)

	// Timestamp
	binary.Write(buf, binary.BigEndian, req.Timestamp)

	// Nonce
	nonceLen := uint16(len(req.Nonce))
	binary.Write(buf, binary.BigEndian, nonceLen)
	buf.Write(req.Nonce)

	return buf.Bytes(), nil
}

// DeserializeLeaseRegisterRequest deserializes bytes to a lease register request
func DeserializeLeaseRegisterRequest(data []byte) (*LeaseRegisterRequest, error) {
	if len(data) < common.SessionIDSize+2 {
		return nil, common.ErrInvalidLength
	}

	req := &LeaseRegisterRequest{}
	pos := 0

	// Session ID
	copy(req.SessionID[:], data[pos:pos+common.SessionIDSize])
	pos += common.SessionIDSize

	// Name
	if pos+2 > len(data) {
		return nil, common.ErrInvalidLength
	}
	nameLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	if pos+nameLen > len(data) {
		return nil, common.ErrInvalidLength
	}
	req.Name = string(data[pos : pos+nameLen])
	pos += nameLen

	// ALPN
	if pos+2 > len(data) {
		return nil, common.ErrInvalidLength
	}
	alpnCount := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	req.ALPN = make([]string, alpnCount)
	for i := 0; i < alpnCount; i++ {
		if pos+2 > len(data) {
			return nil, common.ErrInvalidLength
		}
		alpnLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
		pos += 2
		if pos+alpnLen > len(data) {
			return nil, common.ErrInvalidLength
		}
		req.ALPN[i] = string(data[pos : pos+alpnLen])
		pos += alpnLen
	}

	// Metadata
	if pos+2 > len(data) {
		return nil, common.ErrInvalidLength
	}
	metadataLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	if pos+metadataLen > len(data) {
		return nil, common.ErrInvalidLength
	}
	req.Metadata = string(data[pos : pos+metadataLen])
	pos += metadataLen

	// Public Key
	if pos+2 > len(data) {
		return nil, common.ErrInvalidLength
	}
	pubKeyLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	if pos+pubKeyLen > len(data) {
		return nil, common.ErrInvalidLength
	}
	req.PublicKey = data[pos : pos+pubKeyLen]
	pos += pubKeyLen

	// Expires
	if pos+8 > len(data) {
		return nil, common.ErrInvalidLength
	}
	req.Expires = int64(binary.BigEndian.Uint64(data[pos : pos+8]))
	pos += 8

	// Timestamp
	if pos+8 > len(data) {
		return nil, common.ErrInvalidLength
	}
	req.Timestamp = int64(binary.BigEndian.Uint64(data[pos : pos+8]))
	pos += 8

	// Nonce
	if pos+2 > len(data) {
		return nil, common.ErrInvalidLength
	}
	nonceLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	if pos+nonceLen > len(data) {
		return nil, common.ErrInvalidLength
	}
	req.Nonce = data[pos : pos+nonceLen]

	return req, nil
}

// SerializeLeaseRegisterResponse serializes a lease register response
func SerializeLeaseRegisterResponse(resp *LeaseRegisterResponse) ([]byte, error) {
	buf := make([]byte, 1+common.SessionIDSize+8)

	buf[0] = resp.Status
	copy(buf[1:1+common.SessionIDSize], resp.SessionID[:])
	binary.BigEndian.PutUint64(buf[1+common.SessionIDSize:1+common.SessionIDSize+8], uint64(resp.Expires))

	return buf, nil
}

// DeserializeLeaseRegisterResponse deserializes bytes to a lease register response
func DeserializeLeaseRegisterResponse(data []byte) (*LeaseRegisterResponse, error) {
	if len(data) < 1+common.SessionIDSize+8 {
		return nil, common.ErrInvalidLength
	}

	resp := &LeaseRegisterResponse{
		Status: data[0],
	}
	copy(resp.SessionID[:], data[1:1+common.SessionIDSize])
	resp.Expires = int64(binary.BigEndian.Uint64(data[1+common.SessionIDSize : 1+common.SessionIDSize+8]))

	return resp, nil
}

// SerializeLeaseRefreshRequest serializes a lease refresh request
func SerializeLeaseRefreshRequest(req *LeaseRefreshRequest) ([]byte, error) {
	buf := new(bytes.Buffer)

	// Session ID
	buf.Write(req.SessionID[:])

	// Public Key
	pubKeyLen := uint16(len(req.PublicKey))
	binary.Write(buf, binary.BigEndian, pubKeyLen)
	buf.Write(req.PublicKey)

	// Expires
	binary.Write(buf, binary.BigEndian, req.Expires)

	// Timestamp
	binary.Write(buf, binary.BigEndian, req.Timestamp)

	// Nonce
	nonceLen := uint16(len(req.Nonce))
	binary.Write(buf, binary.BigEndian, nonceLen)
	buf.Write(req.Nonce)

	// Signature
	buf.Write(req.Signature[:])

	return buf.Bytes(), nil
}

// DeserializeLeaseRefreshRequest deserializes bytes to a lease refresh request
func DeserializeLeaseRefreshRequest(data []byte) (*LeaseRefreshRequest, error) {
	if len(data) < common.SessionIDSize+2 {
		return nil, common.ErrInvalidLength
	}

	req := &LeaseRefreshRequest{}
	pos := 0

	// Session ID
	copy(req.SessionID[:], data[pos:pos+common.SessionIDSize])
	pos += common.SessionIDSize

	// Public Key
	if pos+2 > len(data) {
		return nil, common.ErrInvalidLength
	}
	pubKeyLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	if pos+pubKeyLen > len(data) {
		return nil, common.ErrInvalidLength
	}
	req.PublicKey = data[pos : pos+pubKeyLen]
	pos += pubKeyLen

	// Expires
	if pos+8 > len(data) {
		return nil, common.ErrInvalidLength
	}
	req.Expires = int64(binary.BigEndian.Uint64(data[pos : pos+8]))
	pos += 8

	// Timestamp
	if pos+8 > len(data) {
		return nil, common.ErrInvalidLength
	}
	req.Timestamp = int64(binary.BigEndian.Uint64(data[pos : pos+8]))
	pos += 8

	// Nonce
	if pos+2 > len(data) {
		return nil, common.ErrInvalidLength
	}
	nonceLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	if pos+nonceLen > len(data) {
		return nil, common.ErrInvalidLength
	}
	req.Nonce = data[pos : pos+nonceLen]
	pos += nonceLen

	// Signature
	if pos+common.SignatureSize > len(data) {
		return nil, common.ErrInvalidLength
	}
	copy(req.Signature[:], data[pos:pos+common.SignatureSize])

	return req, nil
}

// SerializeLeaseRefreshResponse serializes a lease refresh response
func SerializeLeaseRefreshResponse(resp *LeaseRefreshResponse) ([]byte, error) {
	buf := make([]byte, 1+8)
	buf[0] = resp.Status
	binary.BigEndian.PutUint64(buf[1:9], uint64(resp.Expires))
	return buf, nil
}

// DeserializeLeaseRefreshResponse deserializes bytes to a lease refresh response
func DeserializeLeaseRefreshResponse(data []byte) (*LeaseRefreshResponse, error) {
	if len(data) < 1+8 {
		return nil, common.ErrInvalidLength
	}

	return &LeaseRefreshResponse{
		Status:  data[0],
		Expires: int64(binary.BigEndian.Uint64(data[1:9])),
	}, nil
}

// SerializeLeaseDeleteRequest serializes a lease delete request
func SerializeLeaseDeleteRequest(req *LeaseDeleteRequest) ([]byte, error) {
	buf := new(bytes.Buffer)

	// Session ID
	buf.Write(req.SessionID[:])

	// Public Key
	pubKeyLen := uint16(len(req.PublicKey))
	binary.Write(buf, binary.BigEndian, pubKeyLen)
	buf.Write(req.PublicKey)

	// Timestamp
	binary.Write(buf, binary.BigEndian, req.Timestamp)

	// Nonce
	nonceLen := uint16(len(req.Nonce))
	binary.Write(buf, binary.BigEndian, nonceLen)
	buf.Write(req.Nonce)

	// Signature
	buf.Write(req.Signature[:])

	return buf.Bytes(), nil
}

// DeserializeLeaseDeleteRequest deserializes bytes to a lease delete request
func DeserializeLeaseDeleteRequest(data []byte) (*LeaseDeleteRequest, error) {
	if len(data) < common.SessionIDSize+2 {
		return nil, common.ErrInvalidLength
	}

	req := &LeaseDeleteRequest{}
	pos := 0

	// Session ID
	copy(req.SessionID[:], data[pos:pos+common.SessionIDSize])
	pos += common.SessionIDSize

	// Public Key
	if pos+2 > len(data) {
		return nil, common.ErrInvalidLength
	}
	pubKeyLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	if pos+pubKeyLen > len(data) {
		return nil, common.ErrInvalidLength
	}
	req.PublicKey = data[pos : pos+pubKeyLen]
	pos += pubKeyLen

	// Timestamp
	if pos+8 > len(data) {
		return nil, common.ErrInvalidLength
	}
	req.Timestamp = int64(binary.BigEndian.Uint64(data[pos : pos+8]))
	pos += 8

	// Nonce
	if pos+2 > len(data) {
		return nil, common.ErrInvalidLength
	}
	nonceLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	if pos+nonceLen > len(data) {
		return nil, common.ErrInvalidLength
	}
	req.Nonce = data[pos : pos+nonceLen]
	pos += nonceLen

	// Signature
	if pos+common.SignatureSize > len(data) {
		return nil, common.ErrInvalidLength
	}
	copy(req.Signature[:], data[pos:pos+common.SignatureSize])

	return req, nil
}

// SerializeLeaseDeleteResponse serializes a lease delete response
func SerializeLeaseDeleteResponse(resp *LeaseDeleteResponse) ([]byte, error) {
	return []byte{resp.Status}, nil
}

// DeserializeLeaseDeleteResponse deserializes bytes to a lease delete response
func DeserializeLeaseDeleteResponse(data []byte) (*LeaseDeleteResponse, error) {
	if len(data) < 1 {
		return nil, common.ErrInvalidLength
	}

	return &LeaseDeleteResponse{Status: data[0]}, nil
}

// GenerateNonce generates a random nonce
func GenerateNonce() ([]byte, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return nonce, nil
}
