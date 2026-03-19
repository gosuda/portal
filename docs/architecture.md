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

### Tunnel (`cmd/portal-tunnel`)

- Builds the `portal` CLI and exposes subcommands such as `portal expose` and `portal list`
- Creates one SDK listener per relay through the SDK and consumes one aggregate listener
- Accepts claimed tenant connections from the relay
- Proxies raw TCP to a local target passed to `portal expose`
- Optionally proxies raw UDP to a separate local UDP target passed with `--udp-addr`
- Returns an HTTP 503 response when the local target is unavailable

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

### UDP datagram transport

1. SDK/tunnel registers a lease with `udp_enabled=true`.
2. Relay validates that the datagram plane is enabled and allocates one public UDP port for that lease from the configured `UDP_PORT_MIN` to `UDP_PORT_MAX` range.
3. SDK/tunnel opens an internal QUIC tunnel to the relay on `API_PORT/udp` and authenticates it with a QUIC control stream.
4. A UDP client sends raw UDP packets to the lease `UDPAddr`.
5. Relay receives those packets on the lease UDP port, maps client traffic to a flow ID, and forwards payloads over QUIC DATAGRAM frames.
6. SDK/tunnel receives the datagrams and either:
   - delivers them to application code through `sdk.Exposure.AcceptDatagram()`, or
   - proxies them to a local UDP service in `portal-tunnel`.
7. Replies return over the same QUIC tunnel and are written back to the original client through the lease UDP port.

### UDP Characteristics and Constraints

- UDP support is currently experimental. Expect transport details and operational behavior to keep changing while the model is being validated.
- Public UDP ingress is raw UDP only. Portal does not currently provide public QUIC or HTTP/3 ingress.
- The QUIC tunnel is internal only. It is a relay-to-SDK/tunnel backhaul, not a public client entry point.
- Public UDP and internal QUIC serve different roles:
  - public UDP keeps the external interface compatible with existing raw UDP clients
  - internal QUIC gives the backhaul one outbound, multiplexed, TLS-protected carrier
- Each UDP-enabled lease gets one dedicated public UDP port. The configured UDP port range therefore limits the number of concurrent UDP-enabled leases, not the number of clients behind one lease.
- The public UDP leg is not relay-terminated TLS. Only the internal QUIC backhaul is TLS-protected by default.
- UDP is intended for native UDP clients and services such as game servers, DNS-like protocols, and custom UDP daemons. It is not a browser-native public interface.
- In the current public contract, stream/TCP remains available and UDP is additive when enabled.

## Control Plane Flow

### 1. Register

- `POST /sdk/register`
- JSON envelope response
- Caller provides:
  - `name`
  - `reverse_token`
  - optional `metadata`
  - optional `ttl`
- optional `udp_enabled` (experimental)
- `name` must be a valid single DNS label and relay publishes the lease at `<name>.<root host>`
- Registration reserves the hostname and publishes the route immediately; if no reverse session is ready yet, inbound SNI claims wait up to `ClaimTimeout`
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

## ADRs

- Decision records: [docs/adr/README.md](./adr/README.md)
