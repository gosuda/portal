# WeightManager — Protocol-Agnostic Load Weight Collection

## Overview

`WeightManager` is the component that collects per-protocol load contributions
and fuses them into a single `NodeLoad` for the Reverse Siamese OLS grid
scoring.  It exists to decouple the load-balancing algorithm from the
specifics of any underlying transport protocol (WireGuard, plain TCP, HTTPS,
etc.).

Before `WeightManager`, the OLS engine read WireGuard-specific fields
(`SupportsOverlayPeer`, `OverlayIPv4`) directly from `RelayDescriptor` when
making routing decisions.  This created a hard dependency between the routing
algorithm and the WireGuard implementation.  The new architecture removes that
coupling:

```
Before:
  OLS Engine ──reads──► SupportsOverlayPeer / OverlayIPv4 (WireGuard fields)

After:
  OLS Engine ──uses──► PeerDialer interface
                            ▲
                            └─── server.snapshotPeerDialer (knows WireGuard)
```

---

## Contributor Classification

Contributors fall into two classes determined by their protocol's
observability at the relay layer.

### Immediate contributors — parseable protocols (e.g. HTTP/S)

The relay can parse HTTP/S responses and extract application-level signals:
HTTP status codes, response latencies, and error rates.  These signals are
semantically rich and should be reflected in the load score immediately.

An `HTTPContributor` records each completed request's status code and latency.
It maintains a sliding window of samples and computes P99 latency and error
rate on demand.

```go
// Create and register an HTTP contributor for the control-plane API.
httpC := policy.NewHTTPContributor(30 * time.Second)
server.WeightManager().Register("https-api", httpC.Observe)

// In the HTTP handler (after writing the response):
httpC.RecordRequest(statusCode, latencyMs)
```

### Deferred contributors — opaque transports (e.g. WireGuard, raw TCP)

The relay cannot parse the application payloads of opaque transports; it only
observes the network-layer behaviour: round-trip times, inter-arrival jitter,
and burst intensity.  These are collected by `NetworkContributor`.

```go
// Create and register a network contributor for the WireGuard overlay.
netC := policy.NewNetworkContributor()
server.WeightManager().Register("wireguard-overlay", netC.Observe)

// When an RTT sample is available (e.g. from a keep-alive probe):
netC.RecordRTT(rttMs)

// When burst intensity changes (e.g. derived from queue depth):
netC.RecordBurst(burstScore) // 0.0 = idle, 1.0 = fully saturated
```

Deferred contributors **must never** read WireGuard keys, overlay IP
addresses, or any other transport-internal configuration.  Their
`PartialLoad` fields `ErrorRate` and `P99LatencyMs` must remain zero.

---

## PartialLoad Fields

| Field          | Class     | Description                                           |
|----------------|-----------|-------------------------------------------------------|
| `BurstScore`   | network   | 0–1 burst-traffic intensity (queue/token-bucket fill) |
| `DelayMs`      | network   | Latest observed RTT or one-way delay in ms            |
| `JitterMs`     | network   | RFC 3550 running jitter estimate in ms                |
| `ErrorRate`    | immediate | Fraction of 4xx/5xx responses; 0 if not applicable    |
| `P99LatencyMs` | immediate | 99th-percentile request latency; 0 if not applicable  |

---

## Fusion Algorithm (WeightManager.Collect)

`Collect()` calls every registered contributor and merges their `PartialLoad`
values into a single `NodeLoad`.  Only `AvgLatencyMs` is populated; the
counters (`ActiveConns`, `BytesIn`, `BytesOut`, `ConnRate`) are owned by
`LoadManager` and merged by the caller (`Server.localLoad()`).

Fusion steps:

```
1. base ← mean of all non-zero DelayMs values
           (arithmetic mean over network-observable contributors)

2. IF any contributor reports P99LatencyMs > 0:
       base ← max(base, mean(P99LatencyMs))
   (application-level P99 overrides pure network delay)

3. base += mean(JitterMs) × 0.5
   (jitter penalty: unstable paths appear slower)

4. base += mean(ErrorRate) × 200 ms
   (error-rate penalty: 1% error rate ≈ +2 ms equivalent)

5. base += max(BurstScore) × 50 ms
   (burst penalty: fully saturated path adds up to 50 ms)

result → NodeLoad{AvgLatencyMs: base}
```

The merged `NodeLoad` feeds `OLSManager.UpdateLoad`, where it combines with
the EWMA-smoothed `LoadScore` for the final grid rotation decision.

---

## PeerDialer — Removing WireGuard from the OLS Engine

The OLS engine uses a `PeerDialer` interface for inter-relay forwarding:

```go
// PeerResolver maps a node identity key to a dial address.
type PeerResolver interface {
    PeerAddr(nodeID string) (addr string, ok bool)
}

// PeerDialer resolves node addresses and dials the connection.
type PeerDialer interface {
    PeerResolver
    DialContext(ctx context.Context, network, address string) (net.Conn, error)
}
```

The server owned the WireGuard-specific transport knowledge previously.
With the overlay removed there is no built-in `PeerDialer` implementation,
so transports are free to satisfy the interface however they choose.  This
means:
- The OLS engine (`portal/ols/engine.go`) imports neither `wireguard` nor reads
  `SupportsOverlayPeer` / `OverlayIPv4`.
- Transport-specific code supplies a `PeerDialer` implementation; the engine
  remains protocol-agnostic.
- Future transports (QUIC overlay, raw TCP mesh, etc.) can be added without
  touching the OLS engine.

---

## Integration with LoadManager

```
                ┌─────────────────────────────┐
                │         Server              │
                │                             │
   connections ─►  LoadManager               │
   bytes in/out │    .Snapshot() ────────────►│
                │                             │
   HTTP handler ─►  WeightManager            │
   RTT probes   │    .Collect() ─────────────►│
                │                             │
                │  localLoad() merges both    │
                │  → NodeLoad{               │
                │      ActiveConns, BytesIn,  │
                │      BytesOut, ConnRate,    │
                │      AvgLatencyMs           │─► OLSManager.UpdateLoad
                │    }                        │
                └─────────────────────────────┘
```

`Server.localLoad()`:

```go
func (s *Server) localLoad() policy.NodeLoad {
    load := s.loadMgr.Snapshot()          // counters
    if s.weightMgr != nil {
        wl := s.weightMgr.Collect()       // latency from contributors
        load.AvgLatencyMs = wl.AvgLatencyMs
    }
    return load
}
```

---

## Adding a New Protocol Contributor

1. Decide the contributor class:
   - If your protocol's responses are parseable (e.g. gRPC status codes),
     implement a type similar to `HTTPContributor` and populate `ErrorRate`
     and `P99LatencyMs`.
   - If your protocol is opaque (e.g. a custom binary protocol), use
     `NetworkContributor` and only call `RecordRTT` / `RecordBurst`.

2. Register with the server's `WeightManager`:

   ```go
   myC := NewMyProtocolContributor(...)
   server.WeightManager().Register("my-protocol", myC.Observe)
   ```

3. Call the recording methods from the protocol handler.

4. No changes to `OLSManager`, `Engine`, or `LoadManager` are needed.

---

## File Reference

| File                                    | Role                                              |
|-----------------------------------------|---------------------------------------------------|
| `portal/policy/weight_manager.go`       | WeightManager, PartialLoad, ContributorFunc,      |
|                                         | HTTPContributor, NetworkContributor               |
| `portal/policy/load_manager.go`         | Raw connection/byte counters (unchanged interface)|
| `portal/ols/engine.go`                  | PeerResolver, PeerDialer, Engine (WG-free)        |
| `portal/server.go`                      | snapshotPeerDialer, WeightManager wiring          |
| `portal/policy/ols.go`                  | OLSManager, NodeLoad, computeLoadScore            |
