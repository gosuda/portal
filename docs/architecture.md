# Architecture

## Overview

Portal publishes local services on public subdomains and optional UDP ports through a relay.
Backends connect outward to the relay. Stream traffic is routed by SNI, and tenant TLS remains end-to-end between the client and the SDK/tunnel endpoint for the stream path.

High-level path:

```text
Stream client
  -> Relay SNI listener (:443 by default)
  -> Claimed reverse session
  -> SDK / portal-tunnel
  -> Local service

UDP client
  -> Relay lease UDP port (29900-29999 by default)
  -> Internal QUIC tunnel
  -> SDK / portal-tunnel
  -> Local UDP service
```

## Architecture Invariants

### Transport and Routing

- Raw TCP reverse-connect is the canonical stream transport.
- Do not introduce websocket or legacy compatibility paths unless a new ADR supersedes ADR-0002.
- Derive lease hostnames from the full normalized `PORTAL_URL` host, not from apex extraction.
- Preserve explicit root-host fallback through SNI no-route handling to the admin/API listener.
- Stream ingress is TLS-only. UDP exposure, when enabled, is raw UDP.

### TLS and Identity

- Relay terminates admin/API TLS on the root host and exposes `/v1/sign` for tenant-side keyless signing.
- Relay does not terminate tenant TLS. It peeks ClientHello for SNI and bridges raw encrypted bytes after routing.
- SDK/tunnel endpoints terminate tenant TLS locally with a keyless-backed signer that calls the relay.
- `/sdk/connect`, `/sdk/renew`, and `/sdk/unregister` are authorized by lease existence plus reverse token.
- `/sdk/register` requires the caller to supply the reverse token that later authorizes the lease lifecycle, but registration itself is not separately authenticated by that token.
- Relay URLs must use `https://`.
- HTTP/2 stays disabled on the admin/API TLS listener because `/sdk/connect` depends on HTTP/1.1 hijacking semantics.

### Reverse Session Protocol

- SNI wildcard matching is one level only. `*.parent.example.com` matches `foo.parent.example.com`, not deeper labels.
- Reverse TCP marker bytes remain protocol state:
  - `0x00` = idle keepalive
  - `0x02` = TLS passthrough activation
- `/sdk/connect` remains HTTP/1.1 only.

### JSON and Shared Contract

- All JSON control-plane responses use `APIEnvelope`: `{ ok, data?, error? }`.
- JSON handlers should write responses through the shared API helpers.
- `types/` is reserved for shared wire/public types and cross-package constants only.
- Shared control-plane and public route constants belong in `types/paths.go`.
- Relay-local frontend asset filenames stay local to `cmd/relay-server`.
- Do not import `portal` from `cmd/*` or `sdk` just to reach shared DTOs or constants.

### Operational Constraints

- For non-localhost deployments, ACME management supports only `cloudflare` and `route53`.
- Non-localhost ACME keeps both root and wildcard DNS A records in sync.
- Relay certificate material lives under `KEYLESS_DIR` as `fullchain.pem` and `privatekey.pem`.
- Localhost uses the development certificate path instead of DNS-provider-managed ACME.

## Connection Model

Portal has three distinct network roles:

- **Control-plane HTTP requests**
  - `POST /sdk/register`
  - `POST /sdk/renew`
  - `POST /sdk/unregister`
  - `GET /sdk/domain`
- **Reverse session connection**
  - `GET /sdk/connect?lease_id=...`
  - HTTP/1.1 only
  - hijacked into a long-lived raw TCP session
  - starts idle in the per-lease stream ready queue, then becomes the tenant data path when claimed
- **Internal datagram tunnel**
  - QUIC on `API_PORT/udp`
  - authenticated by a QUIC control stream
  - carries relay-to-SDK/tunnel datagram traffic only

That distinction matters because `/sdk/connect` stops being ordinary HTTP once hijacked, while the UDP backhaul is a separate internal QUIC carrier.

## Core Components

### Relay Server (`cmd/relay-server`)

- Admin/API TLS listener on `--api-port` (default `:4017`)
- SNI listener on `--sni-port` (default `:443`)
- Public frontend routes under `/`, `/app`, `/assets/*`
- Minimal admin surface at `/admin`, `/admin/snapshot`, and admin action/auth routes under `/admin/*`
- Tunnel bootstrap routes at `/install.sh`, `/install.ps1`, and `/install/bin/*`
- Keyless signer endpoint at `/v1/sign`

### Relay Core (`portal/`)

- `Server`: owns listeners, lease registry, API handlers, and shutdown lifecycle
- `routeTable`: exact + single-label wildcard hostname lookup
- `transport.RelayStream`: per-lease ready queue for reverse stream sessions
- `transport.RelayDatagram`: per-lease raw UDP port and datagram backhaul runtime
- `acme`: Cloudflare/Route53-backed root/wildcard A-record sync + certificate provisioning/renewal for the relay root host and wildcard
- `keyless`: admin/API TLS attach helpers and tenant-side signer integration
- `portal/datagram/`: subpackage for QUIC/UDP datagram transport
  - `Session`: owns one active QUIC DATAGRAM connection, decodes frames, exposes `Incoming()` channel
  - `FlowMux`: per-lease QUIC connection manager; multiplexes UDP datagrams using flow IDs over DATAGRAM frames (RFC 9221); embeds a `Session` with dispatch and idle-flow cleanup goroutines
  - `Relay`: binds a public UDP port per lease, bridges between raw UDP sockets and `FlowMux` using `TouchFlow`/`SendDatagram`
  - `PortAllocator`: assigns and recycles per-lease UDP ports from a count-based pool (base port 50000, count via `UDP_PORT_COUNT`) with sticky name-based reservation and grace period
  - `ParseQUICInitialSNI`: decrypts QUIC v1 Initial packets and extracts TLS ClientHello SNI for the QUIC SNI router
- `Server` additionally owns: `quicTunnel` (QUIC listener, ALPN `portal-tunnel`), `quicSNI` (raw UDP PacketConn for QUIC SNI routing), `quicSNIRoutes` (cached FlowMux per source address)

### SDK (`sdk/`)

- `WithDefaultRelayURLs`: fetches the default Portal relay list from the repository-root `registry.json`, appends explicit relay inputs, and normalizes the combined list
- Entry points can opt out of registry defaults and call `utils.NormalizeRelayURLs` directly when they need explicit relay inputs only
- `Listener`: validates one relay URL locally, then starts relay compatibility checks, lease registration, reverse session maintenance, and lease renewal in the background until ready
- `api_client.go`: internal relay client for control-plane requests, reverse session dialing, and internal QUIC tunnel setup
- `ListenerConfig.RetryCount <= 0` means retry forever; positive values close the listener after the retry budget is exhausted
- Default app flow is `WithDefaultRelayURLs -> NewListener -> PublicURL -> http.Server.Serve(listener)` or `WithDefaultRelayURLs -> Expose -> PublicURLs -> http.Server.Serve(exposure)`, with an opt-out path for explicit relay inputs only
- `expose.go`: optional `RunHTTP` helper for serving one handler on both a local HTTP port and the relay listener
- `Expose` keeps one listener per configured relay URL. Relay startup and reconnect failures are retried independently per relay, and successful relays remain available while failed relays keep retrying in the background
- `Exposure.RelayURLs()` returns the configured normalized relay URLs, while `Exposure.PublicURLs()` returns only relays that are currently registered and ready
- Relay-aware entry inspection is reserved for advanced callers such as `portal-tunnel`
- Tenant TLS is created automatically through the relay keyless signer; callers do not provide a local self-signed fallback path
- `Listener` embeds a `datagram.Session` for QUIC datagram transport (no separate UDP listener type)
- `Listener.AcceptDatagram()` / `SendDatagram()`: read/write datagram frames via the session
- `Listener.WaitDatagramReady()`: blocks until relay publishes `udp_addr` and `quic_addr`
- `Exposure.AcceptDatagram()`: receives datagrams from all backing relay listeners with relay context populated on `DatagramFrame`
- `Exposure.SendDatagram()`: sends a datagram frame back through the owning relay listener
- `Exposure.WaitDatagramReady()`: blocks until at least one relay's datagram plane is ready

### Tunnel (`cmd/portal-tunnel`)

- Builds the `portal` CLI and exposes subcommands such as `portal expose` and `portal list`
- Creates one SDK listener per relay through the SDK and consumes one aggregate listener
- Accepts claimed tenant connections from the relay
- Proxies raw TCP to a local target passed to `portal expose`
- Optionally proxies raw UDP to a separate local UDP target when `--udp` is enabled
- Returns an HTTP 503 response when the local target is unavailable
- `--udp` flag (bool, default `false`): enables UDP relay in addition to TCP
- `--udp-addr` flag (string): local UDP target address (`host:port` or port only); required when `--udp` is enabled
- `runUDPBestEffort`: waits for datagram readiness, then calls `proxyExposureDatagrams`
- `proxyExposureDatagrams` (`relays.go`): per-flow UDP sockets to local target with idle cleanup; uses `Exposure.SendDatagram()` for the return path
- Best-effort UDP — failures logged but do not terminate the TCP tunnel

## Transport Model

### Raw reverse transport (TLS only)

1. SDK/tunnel registers one lease per relay with `POST /sdk/register`.
2. SDK opens one or more reverse sessions per registered lease with `GET /sdk/connect?lease_id=...`.
3. Each relay hijacks `/sdk/connect` requests and places the connection in the per-lease stream ready queue.
4. While idle, the relay writes `0x00` keepalive markers.
5. A stream client connects to the relay SNI listener.
6. Relay extracts SNI from ClientHello, resolves a lease, and waits up to `ClaimTimeout` for one reverse session from that lease stream queue.
7. Relay writes `0x02` to activate the claimed session.
8. SDK/tunnel receives `0x02`, starts tenant TLS locally using the relay-backed keyless signer, and the relay bridges raw encrypted bytes end-to-end.

Result: the relay decides routing, but tenant TLS termination still happens at the SDK/tunnel side.

### UDP/QUIC Datagram Transport

1. SDK/tunnel registers a lease with `udp_enabled=true` via `POST /sdk/register`.
2. Relay validates that the datagram plane is enabled (server has `UDP_PORT_COUNT > 0` AND admin has enabled UDP), allocates a UDP port via `PortAllocator`, creates `FlowMux` + `Relay` in `leaseDatagramRuntime`.
3. Response includes `udp_addr` (public UDP endpoint) and `quic_addr` (QUIC tunnel endpoint).
4. SDK opens a QUIC connection to `quic_addr` (ALPN `portal-tunnel`, TLS 1.3, datagrams enabled).
5. Authentication: SDK sends `{lease_id, reverse_token}` JSON on the first QUIC stream; relay validates and calls `FlowMux.Register(conn)`.
6. External UDP client sends a packet to `udp_addr` → `Relay.readLoop` → `FlowMux.TouchFlow` (assigns flow ID) → `FlowMux.SendDatagram` → QUIC DATAGRAM frame.
7. SDK-side `Session.receiveLoop` decodes frame → `Listener.AcceptDatagram()` → `Exposure.AcceptDatagram()` → `proxyExposureDatagrams` → local UDP target.
8. Return path: local response → per-flow read goroutine → `Exposure.SendDatagram()` → `Session.Send` → QUIC DATAGRAM → `FlowMux.runDispatchLoop` → reply callback → `conn.WriteToUDP` to original client.

```text
Client --UDP--> [:50000+ Relay] --DATAGRAM--> [FlowMux/Session] --QUIC--> [Session/Listener] --UDP--> Local Service
                                                                   <--QUIC DATAGRAM return path--
```

Wire format (`types/transport.go`): `[flowID uvarint][payload bytes]`

## Control Plane Flow

### 1. Register

- `POST /sdk/register`
- JSON envelope response
- Caller provides:
  - `name`
  - `reverse_token`
  - optional `metadata`
  - optional `ttl`
  - optional `udp_enabled` (default `false`)
- `name` must be a valid single DNS label and relay publishes the lease at `<name>.<root host>`
- Registration reserves the hostname and publishes the route immediately; if no reverse session is ready yet, inbound SNI claims wait up to `ClaimTimeout`
- When datagram-capable, response includes `udp_addr`, `quic_addr`, and `transport`
- UDP registration requires two conditions: server must have `UDP_PORT_COUNT > 0` AND admin must enable UDP in the admin panel
- `APIErrorCodeUDPDisabled` (HTTP 403) when UDP is disabled by admin policy
- `APIErrorCodeUDPCapacityExceeded` (HTTP 503) when admin-configured max UDP lease limit is reached
- `APIErrorCodeUDPPortExhausted` (HTTP 503) when the UDP port pool is exhausted
- `PORTAL_URL` is normalized to its host component only; path/query segments are ignored for routing

### 2. Reverse Connect

- `GET /sdk/connect?lease_id=...`
- Requires HTTP/1.1
- Requires `X-Portal-Token` header with the lease reverse token
- Relay validates:
  - lease exists and is not expired
  - reverse token matches the registered lease token
- After claim, relay writes `0x02` before switching the session into tenant TLS passthrough
- After hijack, the connection becomes a broker-managed reverse session

### 3. Renew

- `POST /sdk/renew`
- Requires `lease_id` + `reverse_token`
- Extends lease TTL

### 4. Unregister

- `POST /sdk/unregister`
- Requires `lease_id` + `reverse_token`
- Removes the lease, routes, and ready reverse sessions

## Routing Behavior

Route lookup order:

1. Exact hostname match
2. Single-label wildcard match (`*.example.com`)
3. Root-host fallback to the admin/API listener

Notes:

- Wildcards are one level only.
- The exact root host is never served by the wildcard route.
- For non-apex `PORTAL_URL` values such as `https://portal.example.com:8443/admin`, a lease named `demo` is published at `demo.portal.example.com`.

## Admin and Frontend Surface

Current relay-served public routes:

- `/`
- `/app`
- `/app/*`
- `/assets/*`
- `/admin`
- `/admin/snapshot`
- `/admin/leases/*`
- `/install.sh`
- `/install.ps1`
- `/install/bin/*`
- `/healthz`
- `/v1/sign`
- `/sdk/*`

The admin surface is intentionally small in the current Go runtime: an HTML index, one JSON snapshot endpoint, and a small set of admin action/auth routes.

## Shared Contract Surface

Cross-package public contract lives in:

- `types/api.go`
  - API envelope
  - shared request/response DTOs
  - lease metadata
- `types/types.go`
  - shared headers
  - reverse marker constants
- `types/paths.go`
  - shared `/sdk/*`, admin, health, install, and signer paths
- `types/transport.go`
  - `LeaseCapabilities` (Stream/Datagram booleans)
  - `DatagramFrame` wire frame plus SDK relay context
  - `EncodeDatagram` / `DecodeDatagram`
  - Transport constants: `TransportTCP`, `TransportUDP`, `TransportBoth`

Relay-local frontend asset filenames stay in `cmd/relay-server`, not `types/`.

## Keyless and Certificates

- Relay admin/API TLS uses the certificate in `KEYLESS_DIR`
  - `fullchain.pem`
  - `privatekey.pem`
- For non-localhost deployments, ACME DNS-01 currently supports `cloudflare` and `route53`, and keeps:
  - root host A record
  - wildcard host A record
  - relay certificate renewal
- SDK/tunnel fetches the relay certificate chain, verifies it covers tenant hostnames, and uses `/v1/sign` for remote signatures during tenant TLS handshakes

## Design Properties

- Reverse-only backend connectivity
- One canonical raw TCP reverse transport
- Raw public UDP exposure with an internal QUIC datagram backhaul
- SNI-based routing with root-host fallback
- End-to-end tenant TLS with relay-backed keyless signing
- Per-lease reverse token authorization for reverse session lifecycle
- Lease-local stream and datagram ownership through per-lease transport runtimes
- Optional QUIC/UDP datagram transport coexisting with TCP on the same lease
- Per-lease UDP port allocation with sticky name-based reservation
- QUIC tunnel authentication via control stream (lease ID + reverse token)

## ADRs

- Decision records: [docs/adr/README.md](./adr/README.md)
