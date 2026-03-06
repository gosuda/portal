# Greenfield Design: Raw TCP Reverse-Connect + SNI Passthrough + Keyless TLS

## Status

Greenfield design. This document is intentionally not constrained by the current implementation, test suite, or ADR set. It describes the replacement system as if built from scratch.

## Scope

Keep only these product properties:

- Raw TCP reverse-connect from backend to relay
- SNI-based tenant routing
- Tenant TLS passthrough end-to-end
- Keyless TLS for relay-owned admin/API TLS

Everything else is redesignable.

## Goals

- One clear state owner per lease
- No fixed worker pools
- No shared global reverse queue logic beyond lease lookup
- Deterministic connection lifecycle
- Easy to instrument and debug
- Backpressure and failure behavior defined up front

## Non-Goals

- Backward compatibility with the current internal implementation
- Multiple transport modes
- Relay-side tenant TLS termination
- WebSocket support
- HTTP/2 support
- HTTP/1 and HTTP/2 abstraction at the tunnel layer

## High-Level Model

The system has four runtime components:

1. `ControlPlane`
   Validates registration, renewal, unregister, and reverse-connect admission.

2. `RouteTable`
   Maps SNI hostnames to `lease_id`.

3. `LeaseBroker`
   One broker per lease. Owns all idle reverse connections for that lease.

4. `LeaseAgent`
   One agent per SDK listener. Keeps a small target number of idle reverse connections available at the relay.

The key simplification is this:

- Relay global state only resolves `lease_id -> LeaseBroker`
- All reverse connection lifecycle for a lease is owned by that broker
- SDK global state only owns `listener -> LeaseAgent`
- All reverse session lifecycle for a listener is owned by that agent

## Connection Roles

### 1. Browser to Relay

- TCP to relay SNI listener
- Relay peeks ClientHello
- SNI resolves to `lease_id`
- Relay claims one idle reverse connection from that lease broker
- Relay writes `TLSStartMarker`
- Relay bridges raw TCP in both directions

Relay never terminates tenant TLS.

### 2. SDK to Relay

- TCP + TLS to admin/API listener
- HTTP `GET /sdk/connect?lease_id=...`
- Reverse token in header
- On success, connection becomes an idle reverse session owned by the lease broker

### 3. Admin/API TLS

- Relay terminates root-domain TLS only
- Private key stays behind keyless signer

## HTTP Version Policy

Use HTTP/1.1 only everywhere HTTP exists in the system.

### Relay-Owned HTTP

- Admin/API listener is HTTP/1.1 only
- `/sdk/connect` is HTTP/1.1 only
- No HTTP/2 on relay listeners

Reason:

- `/sdk/connect` depends on connection hijacking semantics
- HTTP/2 adds stream multiplexing with no value for this design
- HTTP/2 makes connection behavior harder to reason about operationally

### Tenant-Facing HTTP

Tenant traffic still uses raw TCP + TLS passthrough, so the relay does not enforce HTTP version directly.

Instead, the tenant-side TLS terminator used by the SDK/tunnel must advertise only:

- `http/1.1`

It must not advertise:

- `h2`

Reason:

- HTTP/2 connection coalescing can reuse one TLS connection across multiple subdomains when certificate coverage overlaps
- This design routes once per TCP/TLS connection using SNI
- If multiple origins ride the same HTTP/2 connection, later requests can bypass per-origin SNI routing decisions
- Shared wildcard certificates across tenant subdomains make that risk worse

This greenfield design therefore chooses a hard rule:

- SNI passthrough routing plus shared subdomain certificate coverage implies HTTP/1.1 only

If future work wants tenant HTTP/2, it must first remove cross-tenant certificate overlap or redesign routing away from single-handshake SNI ownership.

## Per-Lease Relay Design

`LeaseBroker` is the only owner of reverse-session state for a lease.

```text
LeaseBroker
  lease_id
  state: active | dropped | stopped
  ready queue: bounded FIFO of idle reverse sessions
  metrics: ready_count, claimed_count, dropped_count, last_claim_at
```

### LeaseBroker API

- `Offer(session) error`
- `Claim(ctx) (*ReverseSession, error)`
- `Drop()`
- `Reset()`
- `Stop()`

### Rules

- `Offer` rejects immediately if broker is dropped or stopped
- `Claim` blocks until a valid idle session is available or timeout/cancel fires
- `Drop` drains ready queue and closes all idle sessions
- `Reset` reopens a dropped lease after successful re-registration
- `Stop` is terminal and used only for process shutdown

No separate global `dropped` map exists outside the broker.

## Reverse Session Design

`ReverseSession` is a single reverse TCP connection plus its state.

```text
states:
  connecting
  admitted
  idle
  claimed
  bridged
  closed
```

### State Transitions

- SDK connects: `connecting -> admitted`
- Broker accepts idle session: `admitted -> idle`
- SNI path claims it: `idle -> claimed`
- Relay writes `TLSStartMarker`: `claimed -> bridged`
- Any close/error: `* -> closed`

### Session Rules

- Keepalive is allowed only in `idle`
- Control marker write is allowed only in `claimed`
- Session close is idempotent
- Close unblocks any waiter

## SDK Design

`LeaseAgent` replaces fixed reverse worker pools.

```text
LeaseAgent
  lease
  relay_url
  target_ready
  current_sessions
  paused: bool
  state: running | paused | stopped
```

### LeaseAgent Behavior

- Maintain `target_ready` idle reverse sessions
- Default `target_ready = 1`
- Optional burst mode may raise to `2` or `4` based on recent claim rate
- No fixed fan-out like `16 workers`

### LeaseAgent Loop

1. If `current_sessions < target_ready`, open one reverse session
2. Complete reverse-connect handshake
3. Wait for `TLSStartMarker`
4. Run backend TLS handshake locally
5. Deliver accepted connection to app `Accept()`
6. Decrement active session count
7. Replenish one new idle session

This is slot-based, not worker-based.

## Protocol

Keep the binary markers minimal:

- `0x00`: idle keepalive
- `0x02`: activate TLS passthrough

No non-TLS tenant mode.

### Reverse Connect Admission

Admission order remains strict:

1. IP policy
2. Lease existence/state
3. Reverse token

Only admitted sessions can enter `LeaseBroker.Offer`.

## API Surface

Keep the existing control-plane shape, simplified internally:

- `POST /sdk/register`
- `GET /sdk/connect?lease_id=...`
- `POST /sdk/renew`
- `POST /sdk/unregister`
- `GET /sdk/domain`
- `POST /v1/sign`

All HTTP endpoints in this list are HTTP/1.1 only.

### Register

- Requires `lease_id`, `name`, `reverse_token`, `tls=true`
- Creates or resets lease broker
- Registers route in `RouteTable`

### Connect

- Admits request
- Hijacks TCP connection
- Creates `ReverseSession`
- Offers it into the lease broker
- Blocks until session is claimed or closed

### Renew

- Extends lease TTL
- Keeps broker active

### Unregister

- Drops broker
- Removes route
- Deletes lease record

## Routing

`RouteTable` owns only routing data:

- exact match
- single-label wildcard match
- portal-root fallback

It does not own reverse session state.

On tenant SNI hit:

1. Resolve `lease_id`
2. Lookup broker
3. `Claim(ctx)`
4. Write `TLSStartMarker`
5. Bridge raw TCP

## Shutdown Semantics

### Lease Drop

- New reverse offers rejected
- Ready queue drained
- In-flight bridged sessions continue until close

### Process Stop

- Control-plane listener stops admitting new reverse sessions
- SNI listener stops admitting new browser sessions
- All brokers enter stopped state
- Idle sessions close immediately
- Bridged sessions close on listener shutdown or peer close

## Failure Handling

### Reverse Connect Rejection

Fatal:

- banned IP
- missing lease
- invalid token
- unsupported transport

Transient:

- relay restart
- temporary dial failure
- temporary route churn

### SDK Agent Rules

- Fatal rejection pauses the agent
- Successful renew/register clears pause
- Transient failure retries with bounded backoff

## Backpressure

Per-lease ready queue is bounded.

Recommended defaults:

- `target_ready = 1`
- `ready_queue_capacity = 8`
- temporary burst increase to `2` or `4`

If the ready queue is full:

- Evict the oldest idle session
- Never evict a claimed or bridged session

## Observability

Every reverse session gets a `conn_id`.

Required structured logs:

- reverse admitted
- reverse offered
- reverse claimed
- TLS marker sent
- bridge started
- bridge ended
- session evicted
- lease dropped
- lease reset
- fatal agent pause

Core metrics:

- ready sessions per lease
- claim wait duration
- reverse connect failures by reason
- session lifetime by state
- first-claim latency after register

## Package Shape

Suggested greenfield package split:

```text
portal/controlplane
portal/routing
portal/broker
portal/session
portal/keyless
sdk/agent
sdk/listener
```

### Ownership

- `controlplane`: admission and API handlers
- `routing`: SNI lookup only
- `broker`: lease-local ready queue and lifecycle
- `session`: reverse session state machine
- `sdk/agent`: maintain target idle sessions
- `sdk/listener`: app-facing `net.Listener`

## Minimal Relay Pseudocode

```text
on_sni_client(server_name):
  lease_id = route_table.lookup(server_name)
  broker = brokers.get(lease_id)
  session = broker.claim(ctx)
  session.send_tls_start()
  bridge(client_conn, session.conn)
```

```text
on_sdk_connect(lease_id, token, conn):
  admit(lease_id, token, client_ip)
  session = new_reverse_session(conn)
  broker = brokers.get_or_create(lease_id)
  broker.offer(session)
  wait_until_claimed_or_closed(session)
```

## Minimal SDK Pseudocode

```text
run_agent():
  while running:
    if active_sessions < target_ready:
      spawn open_one_session()
    wait_for_signal()
```

```text
open_one_session():
  conn = dial_relay()
  do_tls()
  do_http_connect()
  wait_for_tls_start_marker()
  tls_server_handshake()
  deliver_to_accept_queue()
```

## Why This Is Simpler

- Lease-local ownership replaces mixed global and per-connection state
- Slot-based replenishment replaces fixed worker pools
- Reverse lifecycle becomes a state machine instead of loosely coupled goroutines
- Routing, admission, and reverse session storage are separated cleanly

## Recommended Implementation Order

1. Build `LeaseBroker` and `ReverseSession` as isolated packages
2. Build SDK `LeaseAgent` with target-ready semantics
3. Replace relay reverse path behind feature flag or alternate binary
4. Reconnect SNI router to broker claims
5. Reconnect control-plane handlers
6. Add observability before traffic testing

## Cutover Strategy

Best option: separate greenfield binary or branch, not incremental mutation of the current reverse path.

Reason:

- The main value of this design is deleting hidden lifecycle coupling
- Partial migration would preserve too much of the old complexity
