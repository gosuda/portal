# Fast Multipath Routing Specification

## Overview

Portal v2 supports **fast multipath switching** using multiple KCP sessions with built-in FEC. The system maintains parallel paths and quickly switches to the best one based on real-time metrics.

## Architecture

```
+-------------------+     +-------------------+
|   Application    |     | Multipath Router |
|   (KCP Stream) |<--->|   Load Balancer  |
+-------------------+     +-------------------+
                               |      |      |
                               v      v      v
                            +-------+-------+-------+
                            | KCP 1 | KCP 2 | KCP 3 |
                            | Path 1 | Path 2 | Path 3 |
                            +-------+-------+-------+
                               |       |       |
                               v       v       v
                            +-------------------+
                            |   Relay Servers   |
                            +-------------------+
```

## Core Concepts

### 1. Parallel KCP Sessions
- **Multiple active paths**: Each path runs its own KCP session
- **Built-in FEC**: KCP handles forward error correction natively
- **Fast switching**: Switch paths in <100ms without interrupting flow
- **Zero-copy reassembly**: Minimal overhead during path switches

### 2. Path Switching Strategy
- **Proactive monitoring**: Continuously evaluate path quality
- **15% improvement threshold**: Only switch if better by significant margin
- **Fail-fast**: Immediate switch if loss >20% for 2 evaluations
- **5-second cooldown**: Prevent path flapping

### 3. KCP Configuration
```go
kcp.NoDelay(true)           // Disable nodelay for better RTT
kcp.SetMtu(1400)          // Set optimal MTU
kcp.SetWndSize(128, 128)   // Set send/recv window
kcp.SetACKNoDelay(true)      // Enable immediate ACK
kcp.SetResendTimes(2)       // Fast retransmission
kcp.SetFECDataShards(10)   // Enable FEC (default)
kcp.SetFECParityShards(3)  // FEC parity shards
```

## Data Flow

### Send Path (Client → Relay)
```
Application Data
    ↓
[Multipath Router] → Select best path
    ↓
[Active KCP Session] → Send (KCP handles FEC)
    ↓
[Relay Server]
```

### Receive Path (Relay → Client)
```
[KCP Sessions] (from all paths)
    ↓
[Path Demultiplexer] → Identify path by connection
    ↓
[KCP Built-in FEC] → Recover lost packets
    ↓
Application Data
```

## Key Components

### 1. MultipathRouter
**Responsibility**: Path selection and KCP session management
- Track multiple KCP connections to different relays
- Per-path metrics collection
- Path switching decision logic
- KCP session lifecycle

### 2. KCPPath
**Responsibility**: Wrapper around kcp-go session
- Per-path KCP instance
- Path metrics tracking
- Send/Receive handling
- KCP configuration

### 3. RelayGroupClient
**Responsibility**: Multi-relay group management
- Group membership discovery
- Peer-to-peer coordination
- Conflict detection (name/ID mismatches)
- Majority voting for conflict resolution

### 4. ConflictDetector
**Responsibility**: Detect and resolve relay conflicts
- Name conflict: Two relays claim same name
- ID conflict: Different certificates for same derived ID
- Version conflict: Protocol version mismatch
- Timestamp-based resolution

## API Endpoints

### `/v2/relay` (Main multipath endpoint)
**Methods**:
- `POST /v2/relay/connect` - Establish multipath connection
- `POST /v2/relay/send` - Send data (auto-routed)
- `GET /v2/relay/receive` - Receive data
- `POST /v2/relay/switch-path` - Force path switch
- `GET /v2/relay/status` - Path metrics and status

### `/v2/relay/group` (Relay group management)
**Methods**:
- `POST /v2/relay/group/join` - Join relay group
- `POST /v2/relay/group/leave` - Leave relay group
- `POST /v2/relay/group/sync` - Group state sync
- `POST /v2/relay/group/conflict` - Report conflict
- `GET /v2/relay/group/members` - List group members

## Path Selection Logic

### Score Calculation
```
score = latency_ms * (1 + loss_rate * 2.0) + jitter_ms * 0.5
```

### Switching Conditions
1. **Initial selection**: Use path with best score
2. **Cooldown period**: No switches for 5 seconds after last switch
3. **Improvement threshold**: Switch only if 15% better than current
4. **Fail-fast**: Immediate switch if loss >20% for 2 evaluations

### Example
```
Path 1: latency=50ms, jitter=5ms, loss=0.01 → score=60.5
Path 2: latency=80ms, jitter=10ms, loss=0.02 → score=106.8
Path 3: latency=120ms, jitter=15ms, loss=0.03 → score=163.2

→ Select Path 1
```

## Conflict Detection

### Conflict Types
1. **Name Conflict**: `peer.name == my.name && peer.id != my.id`
2. **ID Conflict**: `peer.id == my.id && peer.pubkey != my.pubkey`
3. **Version Conflict**: Protocol version mismatch

### Detection Logic
```go
func DetectConflict(myClaim, peerClaim RelayClaim) *Conflict {
    if peerClaim.Name == myClaim.Name {
        if peerClaim.ID != myClaim.ID {
            return &Conflict{Type: NameConflict}
        } else if !bytes.Equal(peerClaim.PubKey, myClaim.PubKey) {
            return &Conflict{Type: IDConflict}
        }
    }
    return nil
}
```

### Resolution Strategy
1. **Timestamp comparison**: Newer claim wins
2. **Majority voting**: If tie, majority decides
3. **Manual resolution**: Unresolved conflicts require admin action

## Metrics Collection

### Per-Path Metrics
- RTT (round-trip time)
- Jitter (variance in RTT)
- Loss rate (packet loss percentage)
- Bandwidth utilization
- Active connections count

### Global Metrics
- Aggregate throughput across all paths
- Total redundancy overhead
- Path switch frequency
- Conflict detection count
- Average retransmission rate

## Error Handling

### Path Failure
- Immediate detection via KCP timeout
- Automatic exclusion from routing pool
- Re-evaluation after timeout (30s)

### KCP FEC Recovery
- KCP handles FEC natively
- Recovery without retransmission when parity available
- Fallback to retransmission if parity insufficient

### Connection Drop
- Immediate path switch if possible
- Application notification
- Automatic reconnection attempt

## Performance Goals

- **Path setup time**: <500ms per path
- **Path switching time**: <100ms
- **KCP FEC recovery**: <50ms
- **Conflict detection**: <1s
- **Throughput efficiency**: >95% of single best path

## Security Considerations

- **Per-path encryption**: Independent AEAD keys per path
- **Key rotation**: Per-path key phases
- **Group authentication**: Verify member certificates
- **Replay protection**: Timestamp-based rejection
- **Rate limiting**: DoS protection per path

## Configuration

### Default Values
```go
const (
    DefaultPaths        = 3
    MaxPaths            = 8
    MinPaths            = 2
    SwitchCooldown       = 5 * time.Second
    ImprovementThreshold = 0.15  // 15%
    LossFailFast        = 0.20  // 20%
    ConflictTimeout     = 10 * time.Second
    MajorityThreshold   = 0.51  // 51%
)
```

### KCP Defaults
```go
const (
    KCP_MTU            = 1400
    KCP_WND_SIZE       = 128
    KCP_FEC_DATA        = 10  // 10 data shards
    KCP_FEC_PARITY      = 3   // 3 parity shards
    KCP_RESEND_TIMES    = 2
)
```

## Compatibility

- **v1 fallback**: Auto-degrade to single path if v2 unavailable
- **Mixed operation**: v1 and v2 relays can coexist
- **Version negotiation**: Protocol version exchange on connect
- **KCP compatibility**: Works with any KCP-compatible relay

## Benefits Over Erasure Coding

1. **Lower latency**: No encoding/decoding overhead
2. **Simpler implementation**: KCP handles FEC natively
3. **Better performance**: Fast path switching vs. reassembly
4. **Easier maintenance**: No Reed-Solomon dependency
5. **Adaptive behavior**: KCP adjusts FEC parameters dynamically
