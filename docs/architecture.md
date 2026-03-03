# Architecture

## Overview

Portal is a relay network for publishing local services on public subdomains.
The relay is the control and routing plane; service backends connect outward to the relay (NAT-friendly), and clients connect to the relay domain.

High-level path:

```text
Client (Browser)
  -> Relay (:443 SNI router or :4017 HTTP/API)
  -> Reverse tunnel connection
  -> Local service (App/Tunnel host)
```

## Core Components

### Relay Server (`cmd/relay-server`)

- API/Admin server on `--adminport` (default `:4017`)
- SNI router on `--sni-port` (default `:443`)
- SDK registry endpoints under `/sdk/*`
- Keyless signer endpoint at `/v1/sign` (when signer is configured)

### Relay Core (`portal/`)

- `LeaseManager`: lease registration, renew TTL, ban/list policy
- `ReverseHub`: authenticated reverse connection pool per lease
- `sni.Router`: TCP listener that peeks SNI and routes to lease backends
- `acme` + `keyless`: ACME provisioning and remote signing support

### SDK (`sdk/`)

- `Client`: bootstrap relay URLs and optional TLS/keyless setup
- `Listener`: relay-backed `net.Listener` used by apps/tunnel clients
- Shared API types and paths (`/sdk/register`, `/sdk/renew`, etc.)

### Tunnel (`cmd/portal-tunnel`)

- CLI proxy for existing local apps without code changes
- Registers lease via SDK and forwards relay traffic to local `--host`

## Data Plane Modes

### TLS mode (`lease.TLS=true`)

1. Lease is registered with TLS enabled.
2. Relay registers SNI route (`<lease>.<base-domain>`).
3. Client connects via HTTPS to relay SNI port.
4. Relay selects route by SNI and acquires reverse connection from `ReverseHub`.
5. Tunnel-side listener performs TLS handshake (keyless-backed signer), relay forwards raw TCP.

Result: relay does SNI-based routing and forwarding; app payload stays end-to-end encrypted.

### Non-TLS mode (`lease.TLS=false`)

1. Client connects over HTTP to relay/admin port.
2. Relay resolves lease by subdomain and acquires reverse connection.
3. Relay proxies HTTP request/response through reverse tunnel.

Result: simple HTTP proxy path for development or non-TLS services.

## Control Plane Flow

### 1. Register

- App/tunnel posts to `POST /sdk/register` with:
  - `lease_id`
  - `name`
  - `metadata`
  - `tls`
  - `reverse_token`
- Relay stores lease and (TLS only) registers SNI route.

### 2. Reverse Connect

- Backend opens websocket to `GET /sdk/connect?lease_id=...`
- `X-Portal-Reverse-Token` is validated server-side.
- Connection is pooled in `ReverseHub`.

### 3. Renew

- Backend sends `POST /sdk/renew` keepalive.
- Relay refreshes lease TTL and keeps route state current.

### 4. Unregister

- Backend sends `POST /sdk/unregister`.
- Relay removes lease, route, and reverse pool.

## Routing Behavior

`sni.Router` route lookup order:

1. Exact host match
2. Single-label wildcard (`*.example.com`)
3. No-route handler (used for portal root-domain fallback)

Note: wildcard does not match apex domain (`example.com`).

## Keyless and Certificates

- Relay keyless materials are stored in `KEYLESS_DIR`:
  - `fullchain.pem`
  - `privatekey.pem`
- If materials are missing and Cloudflare token is configured, relay can provision via ACME DNS-01.
- SDK/tunnel TLS mode uses keyless signer workflow and `/v1/sign` for remote signatures.

## Important Design Properties

- Reverse-only backend connectivity (no inbound port on app host required)
- Per-lease reverse token authorization
- Separation of control plane (`/sdk/*`) and data plane (SNI/HTTP forwarding)
- Unified lease abstraction for routing, metadata, and lifecycle
