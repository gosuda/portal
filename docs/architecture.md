# Architecture

## Overview

Portal publishes local services on public subdomains through a relay.
Backends connect outward to the relay, the relay routes inbound client traffic by SNI, and tenant TLS remains end-to-end between the browser and the SDK/tunnel endpoint.

High-level path:

```text
Client (Browser)
  -> Relay SNI listener (:443 by default)
  -> Claimed reverse session
  -> SDK / portal-tunnel
  -> Local service
```

## Connection Model

Portal has two distinct network roles:

- **Control-plane HTTP requests**
  - `POST /sdk/register`
  - `POST /sdk/renew`
  - `POST /sdk/unregister`
  - `GET /sdk/domain`
- **Reverse session connection**
  - `GET /sdk/connect?lease_id=...`
  - HTTP/1.1 only
  - hijacked into a long-lived raw TCP session
  - starts idle in the relay broker, then becomes the tenant data path when claimed

That distinction matters because `/sdk/connect` stops being ordinary HTTP once hijacked.

## Core Components

### Relay Server (`cmd/relay-server`)

- Admin/API TLS listener on `--api-port` (default `:4017`)
- SNI listener on `--sni-port` (default `:443`)
- Public frontend routes under `/`, `/app`, `/assets/*`
- Minimal admin surface at `/admin` and `/admin/leases`
- Tunnel bootstrap routes at `/tunnel` and `/tunnel/bin/*`
- Keyless signer endpoint at `/v1/sign`

### Relay Core (`portal/`)

- `Server`: owns listeners, lease registry, API handlers, and shutdown lifecycle
- `routeTable`: exact + single-label wildcard hostname lookup
- `leaseBroker`: per-lease ready queue for reverse sessions
- `reverseSession`: idle keepalive + activation state machine for one reverse TCP connection
- `acme`: Cloudflare/Route53-backed root/wildcard A-record sync + certificate provisioning/renewal for the relay root host and wildcard
- `keyless`: admin/API TLS attach helpers and tenant-side signer integration

### SDK (`sdk/`)

- `RelayClient`: validates one or more relay URLs and owns per-relay HTTP client and raw TLS dial config
- `Listener`: registers one lease per relay, maintains per-entry `readyTarget` reverse sessions, renews lease TTLs, and yields accepted tenant TLS connections through one aggregate listener surface
- Default app flow is `RelayURLs -> NewRelayClient -> Listener -> PublicURLs -> http.Server.Serve(listener)`
- `helper.go`: optional `RunHTTPApp` helper for serving one handler on both a local HTTP port and the relay listener
- Relay-aware entry inspection is reserved for advanced callers such as `portal-tunnel`
- Tenant TLS is created automatically through the relay keyless signer; callers do not provide a local self-signed fallback path

### Tunnel (`cmd/portal-tunnel`)

- Creates one SDK client, registers one lease per relay through the SDK, and consumes one aggregate listener
- Accepts claimed tenant connections from the relay
- Proxies raw TCP to a local `--host`
- Returns an HTTP 503 response when the local target is unavailable

## Transport Model

### Raw reverse transport (`TLS=true` only)

1. SDK/tunnel registers one lease per relay with `POST /sdk/register`.
2. SDK opens one or more reverse sessions per registered lease with `GET /sdk/connect?lease_id=...`.
3. Each relay hijacks `/sdk/connect` requests and places the connection in the per-lease broker ready queue.
4. While idle, the relay writes `0x00` keepalive markers.
5. A browser connects to the relay SNI listener.
6. Relay extracts SNI from ClientHello, resolves a lease, and claims one ready reverse session.
7. Relay writes `0x02` to activate that session.
8. SDK/tunnel receives `0x02`, starts tenant TLS locally using the relay-backed keyless signer, and the relay bridges raw encrypted bytes end-to-end.

Result: the relay decides routing, but tenant TLS termination still happens at the SDK/tunnel side.

## Control Plane Flow

### 1. Register

- `POST /sdk/register`
- JSON envelope response
- Caller provides:
  - `name`
  - `reverse_token`
  - `tls=true`
  - optional `hostnames`
  - optional `metadata`
  - optional `ttl_seconds`
- If no hostname is supplied, relay derives one from `name + root host`
- `PORTAL_URL` is normalized to its host component only; path/query segments are ignored for routing

### 2. Reverse Connect

- `GET /sdk/connect?lease_id=...`
- Requires HTTP/1.1
- Requires `X-Portal-Token` header with the lease reverse token
- Relay validates:
  - lease exists and is not expired
  - reverse token matches the registered lease token
- After hijack, the connection becomes a broker-managed reverse session

### 3. Renew

- `POST /sdk/renew`
- Requires `lease_id` + `reverse_token`
- Extends lease TTL
- Resets a previously dropped broker back to active

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
- For non-apex `PORTAL_URL` values such as `https://portal.example.com:8443/admin`, public lease hosts become `<lease>.portal.example.com`.

## Admin and Frontend Surface

Current relay-served public routes:

- `/`
- `/app`
- `/app/*`
- `/assets/*`
- `/admin`
- `/admin/leases`
- `/tunnel`
- `/tunnel/bin/*`
- `/healthz`
- `/v1/sign`
- `/sdk/*`

The admin surface is intentionally small in the current Go runtime: an HTML index plus a JSON lease list.

## Shared Contract Surface

Cross-package public contract lives in:

- `types/api.go`
  - API envelope
  - shared request/response DTOs
  - lease metadata
  - reverse marker/header constants
- `types/paths.go`
  - shared `/sdk/*`, admin, health, tunnel, and signer paths

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
- SNI-based routing with root-host fallback
- End-to-end tenant TLS with relay-backed keyless signing
- Per-lease reverse token authorization for reverse session lifecycle
- Lease-local reverse session ownership through `leaseBroker`

## ADRs

- Decision records: [docs/adr/README.md](./adr/README.md)
