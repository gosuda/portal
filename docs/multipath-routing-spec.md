# Multipath Routing Specification (V2 Only)

## Overview

Portal v2 provides **fast multipath switching** using multiple concurrent KCP sessions with built-in FEC. The system maintains parallel active paths and rapidly switches to the best one based on real-time metrics. **No v1 compatibility** - pure v2 implementation.

## Architecture

```
+-------------------+     +-------------------+
|   Web Client     |     | Multipath Router |
|  (KCP Sessions) |<--->|  Load Balancer   |
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
                            |  V2 Relay Server |
                            +-------------------+
```

## Core Concepts

### 1. Pure V2 Protocol
- **No v1 fallback**: Only V2 protocol supported
- **Single version**: Simplifies implementation, reduces complexity
- **Clean architecture**: No backward compatibility code

### 2. Concurrent KCP Sessions
- **Multiple active paths**: Each path runs its own KCP session
- **Built-in FEC**: KCP handles forward error correction natively
- **Fast switching**: Switch paths in <100ms without flow interruption
- **Zero-copy reassembly**: Minimal overhead during path switches

### 3. Path Switching Strategy
- **Proactive monitoring**: Continuously evaluate path quality
- **15% improvement threshold**: Only switch if better by significant margin
- **Fail-fast**: Immediate switch if loss >20% for 2 evaluations
- **5-second cooldown**: Prevent path flapping
- **Automatic recovery**: Re-add previously failed paths after cooldown

### 4. Conflict Manager
- **Complete rewrite**: New conflict detection and resolution system
- **Per-relay claims**: Track name and ID claims from all relays
- **Majority voting**: Resolve conflicts by consensus
- **Timestamp comparison**: Newer claims win in ties
- **Conflict quarantine**: Isolate conflicting relays until resolved

## Data Flow

### Send Path (Client → Relay)
```
Application Data
    ↓
[Multipath Router] → Select best path
    ↓
[Active KCP Session] → Send (KCP handles FEC)
    ↓
[V2 Relay Server]
```

### Receive Path (Relay → Client)
```
[V2 Relay Server]
    ↓
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
- Automatic path reconnection

### 2. KCPPath
**Responsibility**: Wrapper around kcp-go session
- Per-path KCP instance with FEC enabled
- Path metrics tracking (RTT, jitter, loss)
- Send/Receive handling with path tagging
- KCP configuration optimization

### 3. ConflictManager
**Responsibility**: Detect and resolve relay conflicts
- Track all relay claims (name, ID, certificate)
- Conflict detection (name/ID/certificate mismatches)
- Resolution by majority voting
- Conflict quarantine management
- Automatic reconciliation when possible

### 4. RelayServer (V2)
**Responsibility**: V2 relay server only (no v1)
- Handle `/v2/relay` endpoints
- Relay group management
- Metrics reporting
- Path telemetry

## API Endpoints

### `/v2/relay` (Main multipath endpoint)
**Methods**:
- `POST /v2/relay/connect` - Establish multipath connection
- `POST /v2/relay/send` - Send data (auto-routed)
- `GET /v2/relay/receive` - Receive data
- `POST /v2/relay/switch-path` - Force path switch
- `GET /v2/relay/status` - Path metrics and status
- `GET /v2/relay/paths` - List all active paths

### `/v2/relay/group` (Relay group management)
**Methods**:
- `POST /v2/relay/group/join` - Join relay group
- `POST /v2/relay/group/leave` - Leave relay group
- `POST /v2/relay/group/sync` - Group state sync
- `POST /v2/relay/group/conflict` - Report conflict
- `GET /v2/relay/group/members` - List group members
- `GET /v2/relay/group/claims` - Get all relay claims
- `POST /v2/relay/group/resolve` - Resolve conflict

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
1. **Name Conflict**: Two relays claim same name, different IDs
2. **ID Conflict**: Different certificates for same derived ID
3. **Certificate Conflict**: Invalid or expired certificate
4. **Version Conflict**: Wrong protocol version (must be v2)

### Detection Logic
```go
func DetectConflict(myClaim, peerClaim RelayClaim) *Conflict {
    if peerClaim.Version != 0x02 {
        return &Conflict{Type: VersionConflict}
    }

    if peerClaim.Name == myClaim.Name {
        if peerClaim.ID != myClaim.ID {
            return &Conflict{Type: NameConflict}
        } else if !bytes.Equal(peerClaim.PubKey, myClaim.PubKey) {
            return &Conflict{Type: CertificateConflict}
        }
    }

    if peerClaim.ID == myClaim.ID && !bytes.Equal(peerClaim.PubKey, myClaim.PubKey) {
        return &Conflict{Type: IDConflict}
    }

    return nil
}
```

### Resolution Strategy
1. **Timestamp comparison**: Newer claim wins
2. **Majority voting**: If tie, majority decides (51% threshold)
3. **Quarantine**: Isolate conflicting relays until resolved
4. **Auto-resolution**: If consensus emerges, automatically resolve
5. **Manual intervention**: Unresolved conflicts after 10 minutes require admin action

## Metrics Collection

### Per-Path Metrics
- RTT (round-trip time)
- Jitter (variance in RTT)
- Loss rate (packet loss percentage)
- Bandwidth utilization
- Active connections count
- FEC recovery rate

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
- Conflict reporting if malicious behavior detected

### KCP FEC Recovery
- KCP handles FEC natively
- Recovery without retransmission when parity available
- Fallback to retransmission if parity insufficient

### Connection Drop
- Immediate path switch if possible
- Automatic reconnection attempt
- Application notification
- Metrics update

## Performance Goals

- **Path setup time**: <500ms per path
- **Path switching time**: <100ms
- **KCP FEC recovery**: <50ms
- **Conflict detection**: <1s
- **Throughput efficiency**: >95% of single best path
- **Scalability**: Support up to 8 concurrent paths

## Security Considerations

- **Per-path encryption**: Independent AEAD keys per path
- **Key rotation**: Per-path key phases
- **Group authentication**: Verify member certificates
- **Replay protection**: Timestamp-based rejection
- **Rate limiting**: DoS protection per path
- **Conflict isolation**: Quarantine conflicting relays

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
    PathRecoveryTimeout = 30 * time.Second
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
    KCP_NO_DELAY        = true
    KCP_ACK_NO_DELAY    = true
)
```

## Testing Components

### 1. Test Relay Server
- **Minimal relay server**: No UI, text-based metrics
- **Path information display**: Show active paths and their metrics
- **Metrics output**: RTT, jitter, loss, bandwidth
- **Debug logging**: Detailed path switching events
- **Conflict reporting**: Show detected conflicts

### 2. Test Client
- **Connect to multiple relays**: Simulate real usage
- **Display path status**: Show current active path
- **Show metrics**: Real-time path performance
- **Manual path switch**: Force switch to specific path
- **Conflict simulation**: Trigger conflicts for testing

## Compatibility

- **V2 only**: No v1 support, pure v2 implementation
- **Single version**: Simplified protocol handling
- **No mixed operation**: All components must be v2
- **Version enforcement**: Reject v1 connections immediately
