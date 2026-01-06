# Portal Protocol v2 Design Document

**Version**: 1.0
**Date**: 2026-01-06
**Status**: Draft
**Authors**: Sisyphus (AI), Oracle Consultant

---

## Table of Contents

1. [Overview](#1-overview)
2. [Core Concepts](#2-core-concepts)
3. [Protocol Layering](#3-protocol-layering)
4. [SerDes Packet Format](#4-serdes-packet-format)
5. [KCP Integration](#5-kcp-integration)
6. [Custom Key Exchange](#6-custom-key-exchange)
7. [Session Management](#7-session-management)
8. [Relay Group Protocol](#8-relay-group-protocol)
9. [Lease Management](#9-lease-management)
10. [WebSocket Control Protocol](#10-websocket-control-protocol)
11. [Connection Metrics & Routing](#11-connection-metrics--routing)
12. [Ed25519 Certificate System](#12-ed25519-certificate-system)
13. [Security Model](#13-security-model)
14. [Directory Structure](#14-directory-structure)
15. [State Machines](#15-state-machines)
16. [Migration Path](#16-migration-path)

---

## 1. Overview

### 1.1 Design Goals

Portal Protocol v2 is a complete rewrite of the relay protocol with the following key improvements:

- **Packet Switching**: Circuit-switched v1 → Packet-switched (I2P-style)
- **Relay Group Consensus**: Simplified majority-based conflict resolution (no Raft for initial implementation)
- **Session Reuse**: No per-connection key exchange; sessions persist across TCP/UDP opens
- **KCP Integration**: Use kcp-go for reliable streams with ChaCha20-Poly1305 AEAD
- **Dynamic Routing**: Latency and loss-aware relay/path selection
- **Complete v1 Separation**: No shared code types with v1

### 1.2 Key Changes from v1

| Aspect | v1 | v2 |
|--------|----|----|
| **Message Format** | Protobuf | Custom binary SerDes |
| **Multiplexing** | Yamux | KCP + SerDes channels |
| **Transport Mode** | Circuit-switched | Packet-switched |
| **Consensus** | Single relay | Relay group majority |
| **Session Keys** | Per-connection | Session ticket-based reuse |
| **Encryption** | ChaCha20-Poly1305 (v1 format) | ChaCha20-Poly1305 (KCP AEAD) |
| **Routing** | Single relay | Multi-path with metrics |

### 1.3 Non-Goals for Initial Implementation

- Raft-based consensus (reserved for future extensibility)
- UDP underlay (initially TCP/WS only)
- Relay mesh forwarding (single-hop to relays)
- CRDT-based conflict resolution

---

## 2. Core Concepts

### 2.1 Identity & Certificate

**IdentityID**: Derived from Ed25519 public key using `cryptoops.DeriveID()` logic.

**CertificateV2**: Self-certifying identity without PKI:

```go
type CertificateV2 struct {
    Version     uint16  // = 2
    ID          string  // DeriveID(pubkey)
    Pubkey      [32]byte // Ed25519
    CreatedAt   uint64  // Unix ms
    ExpiresAt   uint64  // Optional (for rotation)
    Claims      []byte  // Optional: relay flags, group hints
    Signature   [64]byte // Ed25519 sig over above
}
```

**Verification**:
```go
func VerifyCert(cert CertificateV2) error {
    if cert.Version != 2 { return ErrInvalidVersion }
    if DeriveID(cert.Pubkey) != cert.ID { return ErrIDMismatch }
    if !VerifySignature(cert.Pubkey, canonicalBytes(cert), cert.Signature) {
        return ErrInvalidSignature
    }
    return nil
}
```

### 2.2 Lease Mapping

Two keys define a lease:

- **LeaseName**: Human-readable name (DNS-label-like)
- **LeaseID**: Ed25519-derived identity of lease owner

**Invariants** (steady state within relay group):
- `LeaseName → LeaseID` is unique (no name maps to multiple IDs)
- `LeaseID → LeaseName` is unique (no ID maps to multiple names)

### 2.3 Relay Group

**RelayGroup**: Static (or slowly-changing) list of relay peers:

```go
type RelayGroup struct {
    GroupID      [32]byte  // Random at creation
    Members      []RelayMember
    QuorumSize   uint8     // floor(len(Members)/2) + 1
}

type RelayMember struct {
    RelayID      [32]byte  // DeriveID(relay_pubkey)
    IdentityCert CertificateV2
    ControlURL   string    // wss://relay/controlv2
    DataURL      string    // udp://relay:port or wss://relay/datav2
    Region       string    // Optional
    Weight       uint8     // Optional
}
```

### 2.4 Session & Path

**SessionID**: 16-byte identifier stable across relay handovers, derived from X25519 ECDH.

**PathID**: 32-bit identifier per network path/relay hop. Multiple paths can exist per session (multipath).

**Lease TTL**: 30 seconds.

**Refresh Interval**: 20 seconds (client sends refresh 10s before expiry).

---

## 3. Protocol Layering

```
┌─────────────────────────────────────────────────────────────────┐
│                      Application Layer                          │
└─────────────────────────────────────────────────────────────────┘
                            │
┌─────────────────────────────────────────────────────────────────┐
│              SerDes Channels (Stream Multiplexing)              │
│     Multiple logical channels over KCP reliable byte stream      │
└─────────────────────────────────────────────────────────────────┘
                            │
┌─────────────────────────────────────────────────────────────────┐
│                     KCP (Reliable Stream)                       │
│   Congestion control, ARQ, flow control, retransmission          │
│   ChaCha20-Poly1305 AEAD (browser-compatible)                  │
└─────────────────────────────────────────────────────────────────┘
                            │
┌─────────────────────────────────────────────────────────────────┐
│              Custom Key Exchange (SA Establishment)             │
│      X25519 ECDH → HKDF → KCP keys + SerDes keys                │
└─────────────────────────────────────────────────────────────────┘
                            │
┌─────────────────────────────────────────────────────────────────┐
│                    SerDes Packets (Outer)                       │
│     Packet MUX + Latency Probes + Path Identification           │
│     Header: version, type, session_id, path_id, pkt_seq       │
└─────────────────────────────────────────────────────────────────┘
                            │
┌─────────────────────────────────────────────────────────────────┐
│                   Transport (TCP/WebSocket)                     │
└─────────────────────────────────────────────────────────────────┘
```

**Note**: UDP underlay reserved for future; initial implementation uses TCP/WS framing.

---

## 4. SerDes Packet Format

### 4.1 Transport Framing (TCP/WS Compatibility)

Length-delimited datagrams over stream transport:

```
+--------------+----------------+
| Length (u32) | Packet bytes  |
| big-endian   | (Length bytes)|
+--------------+----------------+
```

**DoS Protection**:
- Maximum packet size: 1 MB (2^20 bytes)
- Length == 0xFFFFFFFF is REJECTED (no jumbo frame support)
- Length must be >= 48 (minimum header length)

**Validation order**:
1. Read 4-byte length prefix
2. Validate length range: 48 <= length <= 1MB
3. Reject if length == 0xFFFFFFFF or exceeds max
4. Read exactly `length` bytes

### 4.2 SerDes Header

Fixed 32-byte minimum header (all big-endian):

```
Offset | Field          | Size | Description
-------+----------------+------+----------------------------------------
0      | Magic          | 2    | 0x50 0x32 ("P2")
2      | Version        | 1    | 0x02
3      | Type           | 1    | Packet type (see §4.3)
4      | Flags          | 2    | Bitfield (see §4.4)
6      | HeaderLen      | 2    | Total header bytes (multiple of 4)
8      | Reserved       | 2    | Must be zero
10     | SessionIDHi    | 8    | High 64 bits of 128-bit session ID
18     | SessionIDLo    | 8    | Low 64 bits
26     | PathID         | 4    | Path identifier
30     | PktSeq         | 4    | Per-(session,path) sequence number
34     | SentTimeNs     | 8    | Sender timestamp (monotonic)
42     | PayloadLen     | 2    | Payload size
44     | Padding        | 4    | Reserved (set to zero)
48+    | Extensions     | var  | Optional TLV extensions (see §4.5)
48+    | Payload        | var  | Encrypted data (PayloadLen bytes)
```

### 4.3 Packet Types

| Type | Name | Description |
|------|------|-------------|
| 0x01 | `DATA_KCP` | Raw KCP packet (KCP AEAD-encrypted) |
| 0x02 | `PROBE_REQ` | Latency probe request |
| 0x03 | `PROBE_RESP` | Latency probe response |
| 0x10 | `SA_INIT` | Security Association initialization |
| 0x11 | `SA_RESP` | Security Association response |
| 0x12 | `SA_RESUME` | Session resume (ticket-based) |
| 0x20 | `CTRL_DATA` | Control plane data (future) |

### 4.4 Flags

| Bit | Name | Description |
|-----|------|-------------|
| 0   | ENCRYPTED | Payload is encrypted (must be 1 for DATA_KCP) |
| 1   | KEY_PHASE | Key phase indicator (future rotation) |
| 2   | ACK_ELI | Ack-eliciting hint |
| 3   | HAS_EXT   | Switch extensions present |
| 4   | HAS_TIME  | SentTimeNs is valid |
| 5-7 | Reserved  | Must be zero |

### 4.5 TLV Extensions

TLV format for optional metadata:

```
Type (u8) | Length (u8) | Value (Length bytes)
```

Extension Types:

| Type | Name | Description |
|------|------|-------------|
| 0x01 | `ROUTE_LABEL` | 128-bit routing label (future mesh) |
| 0x02 | `PATH_CLASS` | 1 byte: 0=default, 1=low-latency, 2=bulk |
| 0x03 | `ECN` | Explicit congestion notification (1 byte) |
| 0x04 | `METADATA` | Debug metadata (variable) |

---

## 5. KCP Integration

### 5.1 KCP Configuration

```go
import "github.com/xtaci/kcp-go"

type KCPSession struct {
    conn   *kcp.UDPSession
    crypt  kcp.BlockCrypt // ChaCha20-Poly1305
}

func NewKCPSession(key [32]byte) (*KCPSession, error) {
    // ChaCha20-Poly1305 AEAD (browser-compatible)
    crypt, err := kcp.NewAEADCrypt(
        key[:],
        kcp.ChaCha20Poly1305,
    )
    if err != nil {
        return nil, err
    }

    sess, err := kcp.NewConn2(nil, nil, 0, 1500, crypt)
    if err != nil {
        return nil, err
    }

    // Fast ACK mode for low latency
    sess.SetNoDelay(1, 10, 2, 1)

    // Set MTU (PMTU-based, default 1400)
    sess.SetMtu(1400)

    // Set window sizes
    sess.SetWindowSize(1024, 1024)

    // Stream mode (not datagram mode)
    sess.SetStreamMode(true)

    return &KCPSession{conn: sess, crypt: crypt}, nil
}
```

### 5.2 Key Derivation for KCP

Derived from X25519 ECDH shared secret (see §6):

```go
// shared_secret = X25519(client_eph_priv, relay_eph_pub)
salt := group_id || client_nonce || relay_nonce

prk := hkdf.Extract(sha256.New, shared_secret, salt)

kcpKey := hkdf.Expand(sha256.New, prk, []byte("PORTAL/2 KCP"), 32)
```

### 5.3 KCP Data Flow

1. **Sender**:
   - Application data → SerDes channel (inner multiplexing)
   - SerDes frames → KCP `Write()`
   - KCP encrypts with ChaCha20-Poly1305 → SerDes `DATA_KCP` packet
   - SerDes packet → Transport (TCP/WS)

2. **Receiver**:
   - Transport → SerDes `DATA_KCP` packet
   - SerDes extracts payload → KCP `Input()`
   - KCP decrypts → KCP `Read()`
   - KCP stream → SerDes channel frames → Application

---

## 6. Custom Key Exchange

### 6.1 SA_INIT / SA_RESP Handshake

**Pre-KCP**: Establish Security Association (SA) before initializing KCP.

#### SA_INIT (Client → Relay)

```go
type SAInit struct {
    Protocol       [8]byte   // "PORTAL/2"
    GroupID        [32]byte
    ClientEphPub   [32]byte  // X25519
    ClientIDPub    [32]byte  // Ed25519
    ClientNonce    [16]byte
    ClientSig     [64]byte  // Ed25519 signature
}

// ClientSig = Ed25519_Sign(client_id_priv,
//     "PORTAL/2 SA_INIT" || GroupID || ClientEphPub || ClientNonce)
```

#### SA_RESP (Relay → Client)

```go
type SAResp struct {
    Protocol       [8]byte   // "PORTAL/2"
    GroupID        [32]byte
    RelayEphPub    [32]byte  // X25519
    RelayIDPub     [32]byte  // Ed25519
    RelayNonce     [16]byte
    RelaySig       [64]byte  // Ed25519 signature
    Ticket        []byte     // Encrypted resume ticket
    TicketExpires uint64     // Unix ms
}

// RelaySig = Ed25519_Sign(relay_id_priv,
//     "PORTAL/2 SA_RESP" || GroupID || RelayEphPub ||
//     RelayNonce || ClientNonce || Hash(Ticket))
```

### 6.2 Key Derivation

```go
// ECDH shared secret
shared := X25519(client_eph_priv, relay_eph_pub)

// HKDF salt
salt := append(group_id[:], client_nonce...)
salt = append(salt, relay_nonce...)

// Extract-then-Expand
prk := hkdf.Extract(sha256.New, shared, salt)

// Derive keys
kcpKey := hkdf.Expand(sha256.New, prk, []byte("PORTAL/2 KCP"), 32)
serdesE2EEKey := hkdf.Expand(sha256.New, prk, []byte("PORTAL/2 E2EE"), 32)
sessionID := hkdf.Expand(sha256.New, prk, []byte("PORTAL/2 Session"), 16)
```

### 6.3 Session ID

16-byte identifier derived from shared secret, used for:
- Packet multiplexing across multiple relays
- Session resume with ticket

---

## 7. Session Management

### 7.1 Session Resume Ticket

Encrypted with group ticket key (distributed out-of-band):

```go
type TicketPlain struct {
    Protocol      [8]byte   // "PORTAL/2"
    GroupID       [32]byte
    Epoch         uint64    // Group epoch
    SessionID     [16]byte
    ClientID      [32]byte
    IssuedAt      uint64
    ExpiresAt     uint64
    KCPKeyMaterial [32]byte
    Constraints   TicketConstraints
}

type TicketConstraints struct {
    MaxPathCount  uint8
    AllowedRelayIDs [][32]byte
}

type Ticket struct {
    Encrypted []byte  // AES-GCM(ticket_plain)
    AuthTag   [16]byte
}
```

### 7.2 SA_RESUME Flow

**Client → Relay**:

```go
type SAResume struct {
    Protocol     [8]byte   // "PORTAL/2"
    Ticket       []byte    // Encrypted ticket
    PathID       uint32
    ClientNonce  [16]byte
    ClientSig    [64]byte  // Ed25519 over above
}
```

**Relay**:
1. Decrypt ticket with group ticket key
2. Verify epoch, expiry, constraints
3. Derive KCP keys from ticket
4. Resume or create session

---

## 8. Relay Group Protocol

### 8.1 Peer Conflict Checking

When relay receives `LeaseRegister` for `(name, lease_id)`:

1. **Local check**:
   - If `name` already maps to different `lease_id` → conflict
   - If `lease_id` already maps to different `name` → conflict

2. **Peer check**: Query all group members:
   - `PeerLeaseQueryRequest { name?, lease_id? }`
   - Wait up to `PeerQueryTimeout` (250ms baseline, 500ms cross-region)

3. **Vote counting**:
   - Each peer responds with current mapping (or "unknown")
   - Self + peers form candidate votes

### 8.2 Majority Resolution

Let **G** = group size, **Q = floor(G/2)+1** (quorum).

For each candidate mapping (e.g., `(name -> lease_id)`):

- **Score(C)** = count of responders reporting exactly C as existing

**Decision rules**:

1. If `Score(proposed) >= Q`: Strong majority → accept
2. Else if no candidate reaches Q:
   - If proposed has highest score and `Score >= 2` (self + 1 peer): weak majority → accept
   - Else if tie (multiple candidates with equal max score):
     - Choose lexicographically smallest `lease_id`
     - Reject others
3. Else if insufficient responders (< 2 for weak majority): Return `UNRESOLVED`

### 8.3 Peer Query Protocol

**Request** (Relay → Peer):

```go
type PeerLeaseQueryRequest struct {
    QueryID   uint64    // Echoed in response
    Name      []byte    // Optional (max 255 bytes)
    LeaseID   [32]byte  // Optional
}
```

**Response** (Peer → Relay):

```go
type PeerLeaseQueryResponse struct {
    QueryID   uint64
    Mappings  []LeaseMapping
    ViewEpoch uint64    // Peer's view epoch
    Timestamp uint64    // For debugging
}

type LeaseMapping struct {
    Name      []byte
    LeaseID   [32]byte
    ExpiresAt uint64
}
```

### 8.4 Relay Group Info Distribution

Clients obtain relay group info via `RelayGroupInfoResponse`:

```go
type RelayGroupInfoResponse struct {
    GroupID      [32]byte
    QuorumSize   uint8
    Members      []RelayMember
    GroupEpoch   uint64
}
```

---

## 9. Lease Management

### 9.1 Lease Registration Flow

```
Client                    Relay R0               Peers
  |                         |                      |
  |-- LeaseRegisterReq ---->|                      |
  | (name, lease_id,       |                      |
  |  proof_sig)            |                      |
  |                         |                      |
  |                         |-- PeerLeaseQuery --->|
  |                         | (parallel to all)    |
  |                         |<-- PeerLeaseResp ---|
  |                         | (collect votes)      |
  |                         |                      |
  |<--- LeaseRegisterResp --|                      |
  | (OK / CONFLICT /       |                      |
  |  UNRESOLVED)           |                      |
```

### 9.2 Lease Registration Request

```go
type LeaseRegisterRequest struct {
    Name         []byte    // Max 255 bytes
    LeaseID      [32]byte  // DeriveID(owner_pubkey)
    ExpiresAt    uint64    // now + 30s
    ALPNs        []string  // Supported ALPNs
    Metadata     []byte    // Optional
    ClientSeq    uint64    // Per-connection sequence
    Timestamp    uint64    // Unix ms
    ProofSig     [64]byte  // Ed25519(owner_priv,
                            // "LEASE_REGISTER" || Name || LeaseID ||
                            // ExpiresAt || Metadata)
}
```

### 9.3 Lease Registration Response

```go
type LeaseRegisterResponse struct {
    Status       uint8     // OK, CONFLICT, UNRESOLVED, UNAUTHORIZED
    ExpiresAt    uint64    // If OK
    Winner       *WinnerMapping  // If CONFLICT
    ObservedVotes VoteSummary
    RetryAfter   uint32    // ms, if UNRESOLVED
}

type WinnerMapping struct {
    Name     []byte
    LeaseID  [32]byte
    ExpiresAt uint64
}

type VoteSummary struct {
    TotalPeers   uint8
    Responded    uint8
    VotesPerID   map[[32]byte]uint8  // lease_id -> count
}
```

### 9.4 Lease Update / Refresh Cycle

- **Lease TTL**: 30 seconds
- **Refresh Interval**: 20 seconds (10s before expiry)

**LeaseRefreshRequest**:

```go
type LeaseRefreshRequest struct {
    LeaseID      [32]byte
    Name         []byte
    NewExpiresAt uint64    // now + 30s
    ClientSeq    uint64
    ProofSig     [64]byte  // Same format as Register
}
```

**Refresh Behavior**:

1. Relay performs local check only (no peer query by default)
2. Extend expiry if mapping exists
3. Trigger peer query if:
   - Local conflict observed, OR
   - Group membership changed, OR
   - Every 3rd refresh (~60s) for convergence

### 9.5 Lease Expiry Handling

If lease expires before refresh:

- Client receives `NOT_FOUND` response
- Client performs full `LeaseRegisterRequest` (including peer conflict check)

---

## 10. WebSocket Control Protocol

### 10.1 Transport Framing

Binary framing over WebSocket:

```
+--------------+-------------------+
| Length (u32) | Message bytes     |
| big-endian   | (Length bytes)    |
+--------------+-------------------+
```

### 10.2 Connection Handshake

**ServerHello** (Relay → Client):

```go
type ServerHello struct {
    ServerNonce   [32]byte
    ServerTime    uint64
    RelayCert     CertificateV2
    SupportedVersions []uint16
}
```

**ClientHello** (Client → Relay):

```go
type ClientHello struct {
    ClientCert    CertificateV2
    ClientNonce   [32]byte
    Signature    [64]byte  // Ed25519 over:
                           // "PORTAL/2 CLIENTHELLO" ||
                           // ServerNonce || ClientNonce ||
                           // ClientCert || ServerCert
}
```

**Replay Protection**:
- Server issues unique `ServerNonce` per connection
- Client signature binds to `ServerNonce`
- Subsequent messages include `ClientSeq` (monotonic)

### 10.3 Message Types

| Type | Name | Direction |
|------|------|-----------|
| 0x01 | `RELAY_GROUP_INFO_REQ` | Client → Relay |
| 0x02 | `RELAY_GROUP_INFO_RESP` | Relay → Client |
| 0x03 | `LEASE_LOOKUP_REQ` | Client → Relay |
| 0x04 | `LEASE_LOOKUP_RESP` | Relay → Client |
| 0x05 | `LEASE_REGISTER_REQ` | Client → Relay |
| 0x06 | `LEASE_REGISTER_RESP` | Relay → Client |
| 0x07 | `LEASE_REFRESH_REQ` | Client → Relay |
| 0x08 | `LEASE_REFRESH_RESP` | Relay → Client |
| 0x09 | `LEASE_DELETE_REQ` | Client → Relay |
| 0x0A | `LEASE_DELETE_RESP` | Relay → Client |

### 10.4 Lease Lookup (Fastest Relay Selection)

**LeaseLookupRequest**:

```go
type LeaseLookupRequest struct {
    Name    []byte
}
```

**LeaseLookupResponse**:

```go
type LeaseLookupResponse struct {
    Status      uint8    // OK, NOT_FOUND
    LeaseID     [32]byte
    ExpiresAt   uint64
    ObservedAt  uint64
    RTTEcho     uint64    // Optional: for measurement
}
```

**Fastest Relay Selection**:

- Client maintains `LatencyWindow[16]` per relay
- Select relay with lowest `score = avg_rtt_ms * (1 + loss_rate*2.0)`
- If no metrics: race multiple relays, first valid response wins
- Cache metrics for ~2 minutes

---

## 11. Connection Metrics & Routing

### 11.1 Sliding Window [16] Average

```go
type LatencyWindow struct {
    samples [16]uint64  // milliseconds
    idx     uint8
    count   uint8
    sum     uint64
}

func (w *LatencyWindow) Add(sample uint64) {
    if w.count < 16 {
        w.count++
    } else {
        w.sum -= w.samples[w.idx]
    }
    w.samples[w.idx] = sample
    w.sum += sample
    w.idx = (w.idx + 1) % 16
}

func (w *LatencyWindow) Average() uint64 {
    if w.count == 0 {
        return 0
    }
    return w.sum / uint64(w.count)
}
```

### 11.2 Packet Loss Rate

Per-path loss tracking:

```go
type LossTracker struct {
    sent      uint32
    acked     uint32
    lost      uint32
    lossEWMAs float32  // Exponential moving average
    beta      float32   // Smoothing factor (0.1)
}

func (lt *LossTracker) RecordAck() {
    lt.acked++
}

func (lt *LossTracker) RecordLoss() {
    lt.lost++
    sample := float32(lt.lost) / float32(lt.lost+lt.acked)
    lt.lossEWMAs = lt.lossEWMAs*(1-lt.beta) + sample*lt.beta
}

func (lt *LossTracker) LossRate() float32 {
    if lt.lost+lt.acked == 0 {
        return 0
    }
    return lt.lossEWMAs
}
```

**Loss detection**:
- Packet considered lost if not ACKed within `loss_timeout`
- `loss_timeout = max(3*RTT_avg, 200ms, min(2s))`

### 11.3 RTT Monitoring

Every SerDes packet includes `SentTimeNs` (sender timestamp):

```go
type Packet struct {
    // ...
    SentTimeNs uint64  // Sender's monotonic time
    PktSeq     uint32  // Per-(session,path) sequence
}
```

**ACK Echo** (receiver echoes sequence and timestamps):

```go
type PacketAck struct {
    PktSeq      uint32
    RecvTimeNs  uint64  // Receiver's monotonic time
    SendTimeNs  uint64  // Echoed from original packet
}
```

**RTT Calculation**:

```
sender_now - SendTimeNs = One-way + processing + return
```

Use sliding window [16] average for smoothed RTT.

### 11.4 Routing Score & Switching

**Per-path score**:

```
score = latency_ms * (1 + loss_rate * 2.0) + jitter_ms * 0.5
```

**Switching Policy**:

- Evaluate every `T = 1s` (or every 16 packets)
- Switch only if:
  - Candidate score improvement >= **Δ = 15%** AND
  - Candidate has >= 8 samples
  - Cooldown expired (5s after last switch)
- Fail-fast: if `loss_rate > 0.2` for 2 evaluations → immediately try next relay

**Jitter calculation**:

```go
type JitterWindow struct {
    samples [16]uint64
    idx     uint8
    count   uint8
}

func (j *JitterWindow) Add(sample uint64) {
    // ... (similar to LatencyWindow)
}

func (j *JitterWindow) Average() uint64 {
    // Average absolute difference between consecutive samples
    // ...
}
```

---

## 12. Ed25519 Certificate System

### 12.1 Derivation from cryptoops

Reuse `portal/core/cryptoops/` logic (conceptually):

```go
// Re-exported in portal/corev2/identity/derive.go
func DeriveID(pubkey [32]byte) string {
    // Same derivation as v1 cryptoops.DeriveID()
    // e.g., base58(sha256(pubkey)[0:20])
    return deriveIDV1(pubkey)
}
```

### 12.2 Certificate Format

```go
type CertificateV2 struct {
    Version     uint16  // = 2
    ID          string  // DeriveID(Pubkey)
    Pubkey      [32]byte // Ed25519
    CreatedAt   uint64  // Unix ms
    ExpiresAt   uint64  // Optional (0 = no expiry for identity)
    Claims      []byte  // Optional: relay flags
    Signature   [64]byte // Ed25519 sig over above (excluding sig)
}
```

**Canonical bytes for signature**:
```
version (2) || id || pubkey || created_at || expires_at || claims
```

### 12.3 Certificate Distribution

- Clients obtain relay certs via `ServerHello`
- Relay group member certs provided in `RelayGroupInfoResponse`
- Certs cached by `ID` locally

### 12.4 Certificate Updates

Relays can rotate keys by:

1. Publishing new cert in group info alongside old cert
2. Maintaining grace period (e.g., 5 minutes)
3. Clients accepting both old and new certs during grace period

No separate `/certificate` endpoint; all cert exchange over WebSocket control channel.

---

## 13. Security Model

### 13.1 Threat Model

| Threat | Impact | Mitigation |
|--------|--------|------------|
| **Split-brain mapping** | Name maps to different IDs in partitions | Short TTL (30s) + deterministic tie-breaker + eventual convergence |
| **Malicious relay lying** | Fabricate peer votes | Client can query multiple relays, prefer decisions with more responses |
| **Replay attacks** | Replay old control messages | Server nonce + per-connection sequence + timestamp validation |
| **Lease hijacking** | Steal name ownership | Ed25519 proof-of-ownership signature required |
| **Compromised relay** | MITM, data tampering | AEAD encryption (ChaCha20-Poly1305) at KCP layer |
| **Equivocation** | Relay signs conflicting leases | Per-relay `(epoch,name)→proposal_hash` cache for vote TTL |
| **DoS (memory exhaustion)** | Oversized packets crash relay | Strict packet size limits: max 1MB, reject oversized frames |
| **DoS (resource amplification)** | Small request causes huge response | No amplification: responses bounded by request size + fixed overhead |

### 13.2 Authentication

**Mutual authentication**:
- Client authenticates with Ed25519 signature over handshake
- Relay authenticates via `CertificateV2`
- All mutating requests (Register, Refresh, Delete) require signature

**Peer authentication**:
- Relays authenticate peer messages with relay identity signatures
- No PKI required; self-certifying certs

### 13.3 Replay Protection

**Control channel**:
- Handshake: `ServerNonce` binds all signatures
- Messages: `ClientSeq` (monotonic), reject `seq <= last_seen`

**Data channel**:
- Per-path packet sequence numbers
- AEAD replay window (KCP internal)

### 13.4 Confidentiality & Integrity

- **KCP layer**: ChaCha20-Poly1305 AEAD encrypts all application data
- **Relays cannot decrypt**: KCP keys derived from E2EE (client ↔ app)
- **Integrity**: AEAD tag protects against tampering

---

## 14. Directory Structure

```
portal/corev2/
├── identity/
│   ├── cert.go          # CertificateV2 format, verify, serialize
│   ├── derive.go        # ID derivation (calls cryptoops)
│   └── signature.go     # Ed25519 signature helpers
├── serdes/
│   ├── packet.go        # SerDes packet encoder/decoder
│   ├── types.go         # Header, flags, types
│   ├── probe.go         # Latency probes
│   └── channel.go       # Inner SerDes channel multiplexing
├── kcpwrapper/
│   ├── session.go       # KCP session wrapper
│   ├── crypto.go        # ChaCha20-Poly1305 setup
│   └── keys.go         # HKDF key derivation
├── handshake/
│   ├── sa_init.go       # SA_INIT/SA_RESP
│   ├── sa_resume.go     # SA_RESUME with ticket
│   └── keyderivation.go # HKDF for KCP, SerDes, SessionID
├── session/
│   ├── ticket.go        # Ticket encryption/decryption
│   └── resumption.go    # Session resume logic
├── control/
│   ├── ws_server.go     # WebSocket control server
│   ├── ws_client.go     # WebSocket control client
│   ├── frame.go         # Length+type framing
│   ├── messages/        # Message structs
│   │   ├── relay_group.go
│   │   ├── lease.go
│   │   └── handshake.go
│   └── auth.go         # Signature validation
├── lease/
│   ├── store.go         # In-memory store + TTL wheel
│   ├── conflict.go      # Conflict detection + majority resolution
│   ├── refresh.go       # Refresh policy
│   └── types.go
├── relaygroup/
│   ├── group.go         # Membership, quorum
│   ├── peer_client.go   # Peer query client
│   └── peer_server.go   # Peer query handler
├── metrics/
│   ├── window16.go      # Sliding window [16] average
│   ├── loss.go         # Loss tracking
│   ├── jitter.go       # Jitter calculation
│   └── score.go        # Routing score
├── routing/
│   ├── selector.go      # Relay/path selection
│   ├── cooldown.go      # Switch cooldown
│   └── decision.go      # Routing decision logic
└── transport/
    ├── kcp.go          # KCP integration
    ├── aead.go         # ChaCha20-Poly1305 wrapper
    └── framing.go      # TCP/WS datagram framing
```

**v1 Separation**:
- No shared types with `portal/` or `portal/core/`
- May call `portal/core/cryptoops/` helpers (re-exported in v2 namespace)
- Completely independent protocol and message formats

---

## 15. State Machines

### 15.1 Lease Registration with Conflict Resolution

```
                    INIT
                      |
                VALIDATE_PROOF
                      |
              +-------+-------+
              | (fail)       | (pass)
              v               v
        RESPOND_UNAUTH   LOCAL_CHECK
                                |
                      +---------+----------+
                      | (conflict?)       | (no conflict)
                      v                   v
                    PEER_QUERY          COMMIT
                      |
                    DECIDE
            +-----------+-----------+
            |                       |
    (winner=proposed)      (winner!=proposed)
            |                       |
            v                       v
          COMMIT           RESPOND_CONFLICT
            |
      RESPOND_OK
```

### 15.2 Connection Routing Decision

```
                COLLECT_SAMPLES
                      |
                EVALUATE (every T)
                      |
           +----------+----------+
           |                     |
     (improvement < Δ)    (improvement >= Δ
           |                  && cooldown ok)
           |                     |
           v                     v
     (no action)            SWITCHING
                                   |
                              COOLDOWN
                                   |
                                   v
                             EVALUATE (after 5s)

Special: loss_rate > 0.2 for 2 ticks → immediate SWITCHING
```

### 15.3 Lease Refresh Cycle

```
               REGISTERED
                    |
              REFRESH_DUE (t=20s)
                    |
               REFRESH_SENT
                    |
          +----------+----------+
          |                     |
      REFRESH_OK           TIMEOUT
          |                     |
          v                     v
    REGISTERED           REFRESH_DUE
    (extend expiry)      (retry)

REFRESH_CONFLICT → REREGISTER (choose new name)
EXPIRED → REREGISTER
```

---

## 16. Migration Path

### 16.1 v1 → v2 Coexistence

Initial rollout strategy:

1. **Dual-stack relays**: Support both v1 and v2 protocols
2. **Separate control endpoints**:
   - `wss://relay/control` (v1, protobuf)
   - `wss://relay/controlv2` (v2, binary framed)
3. **SDK/app gradual migration**:
   - Prefer v2 if both ends support
   - Fallback to v1 otherwise

### 16.2 Feature Detection

Clients can detect v2 support by:

1. Attempting to connect to `/controlv2` endpoint
2. Checking `ServerHello.SupportedVersions`
3. Falling back to v1 if `/controlv2` unavailable

### 16.3 Breaking Changes

**Not backward compatible**:
- Different packet format (protobuf → SerDes)
- Different multiplexing (yamux → KCP)
- Different key exchange format
- Different lease management (single relay → relay group)

**Migration requires**:
- Both client and server to support v2 simultaneously
- Configuration update for endpoint URLs
- Graceful period for v1 deprecation

---

## Appendix A: Constants

```go
const (
    // Protocol
    ProtocolMagic   = "P2"
    ProtocolVersion = 0x02
    ProtocolString  = "PORTAL/2"

    // Packet types
    TypeDataKCP    = 0x01
    TypeProbeReq   = 0x02
    TypeProbeResp  = 0x03
    TypeSAInit     = 0x10
    TypeSAResp     = 0x11
    TypeSAResume   = 0x12
    TypeCtrlData   = 0x20

    // Message types (WebSocket)
    MsgRelayGroupInfoReq  = 0x01
    MsgRelayGroupInfoResp = 0x02
    MsgLeaseLookupReq     = 0x03
    MsgLeaseLookupResp    = 0x04
    MsgLeaseRegisterReq   = 0x05
    MsgLeaseRegisterResp  = 0x06
    MsgLeaseRefreshReq    = 0x07
    MsgLeaseRefreshResp   = 0x08
    MsgLeaseDeleteReq     = 0x09
    MsgLeaseDeleteResp    = 0x0A

    // Status codes
    StatusOK         = 0x00
    StatusNotFound   = 0x01
    StatusConflict   = 0x02
    StatusUnresolved = 0x03
    StatusUnauthorized = 0x04
    StatusInvalidArgument = 0x05
    StatusRateLimited   = 0x06

    // Timeouts
    PeerQueryTimeout    = 250 * time.Millisecond
    RoutingEvalInterval = 1 * time.Second
    SwitchCooldown      = 5 * time.Second
    LossTimeoutBase     = 200 * time.Millisecond
    LossTimeoutMax      = 2 * time.Second

    // Lease
    LeaseTTL        = 30 * time.Second
    RefreshInterval = 20 * time.Second

    // Metrics
    LatencyWindowSize = 16
    RoutingDeltaPct  = 15    // 15% improvement required
    LossThreshold    = 0.20  // 20% loss triggers fail-fast
    JitterAlpha      = 0.5   // Jitter coefficient

    // Sizes
    SessionIDSize = 16
    GroupIDSize   = 32
    RelayIDSize   = 32
    SignatureSize = 64

    // Packet limits (DoS protection)
    MaxPacketSize = 1 << 20  // 1 MB maximum
    MinPacketSize = 48         // Minimum header length
    JumboFrameMarker = 0xFFFFFFFF  // REJECTED (no jumbo support)
)
```

---

## Appendix B: Example Message Flow

### B.1 Complete Session Establishment

```
Client                       Relay
  |                             |
  |-- WebSocket connect --------->|
  |                             |
  |<-- ServerHello --------------|
  |  (server_nonce, relay_cert)  |
  |                             |
  |-- ClientHello -------------->|
  |  (client_cert, signature)    |
  |                             |
  |-- SA_INIT ------------------>|
  |  (eph_pub, nonce, sig)       |
  |                             |
  |<-- SA_RESP ------------------|
  |  (eph_pub, nonce, sig,       |
  |   ticket)                    |
  |                             |
  |-- DATA_KCP (encrypted) ---->|
  |  (KCP stream)               |
  |<-- DATA_KCP (encrypted) ----|
  |                             |
```

### B.2 Lease Registration with Conflict Resolution

```
Client          Relay R0         Peer A          Peer B
  |                 |               |               |
  |-- RegisterReq -->|               |               |
  | (name X,        |               |               |
  |  lease_id I1)   |               |               |
  |                 |               |               |
  |                 |-- PeerQuery -->|               |
  |                 |-- PeerQuery ------------------>|
  |                 |               |               |
  |                 |<-- PeerResp --|               |
  |                 |<-- PeerResp -------------------|
  |                 |               |               |
  |                 | (collect votes)              |
  |                 |                               |
  |<-- RegisterResp --|               |               |
  |  OK             |               |               |
  |                 |               |               |
```

---

## Change Log

| Version | Date | Changes |
|---------|------|---------|
| 1.0 | 2026-01-06 | Initial design document |

---

**Document Status**: Draft
**Next Review**: After initial implementation phase
