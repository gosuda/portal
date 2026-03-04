# Architecture

## Overview

Portal is a relay network for publishing local services on public subdomains.
The relay is both the control plane and routing plane. Service backends connect outward to the relay (NAT-friendly), and clients connect to the relay domain.

High-level path:

```text
Client (Browser)
  -> Relay (:443 SNI router or :4017 HTTP/API)
  -> Reverse tunnel connection
  -> Local service (App/Tunnel host)
```

## Connection Responsibilities

- Conn #1 (`browser -> app`) is the tenant-facing data plane.
  - Data-plane TLS behavior remains end-to-end between client and app/tunnel host.
  - Relay forwards tenant traffic and does not replace app identity policy.
- Conn #2 (`relay -> tunnel`) is the control plane.
  - `/sdk/register`, `/sdk/connect`, `/sdk/renew`, and `/sdk/unregister` require lease-bound client mTLS identity.
  - Control-plane admission order is strict and deterministic: `IP -> Lease -> CertBind -> Token`.
  - Legacy non-mTLS clients are rejected at admission (hard-break behavior).

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

Anti-abuse policy is driven from admin-managed state and applied consistently for both registration and reverse admission.

### SDK (`sdk/`)

- `Client`: bootstrap relay URLs and optional TLS/keyless setup
- `Listener`: relay-backed `net.Listener` used by apps/tunnel clients
- Shared API types and paths (`/sdk/register`, `/sdk/renew`, etc.)

### Tunnel (`cmd/portal-tunnel`)

- CLI proxy for existing local apps without code changes
- Registers lease via SDK and forwards relay traffic to local `--host`

## Transport Model

### Raw reverse transport (`lease.TLS=true` only)

1. Relay requires registered leases to use TLS.
2. Backend opens a raw TCP reverse connection via `GET /sdk/connect?lease_id=...`.
3. Clients connect via HTTPS to relay SNI port.
4. Relay resolves route by SNI and acquires a reverse connection from `ReverseHub`.
5. Tunnel-side listener performs TLS handshake (keyless-backed signer), while relay forwards raw TCP transparently.
6. No alternate control-plane transport is supported; relay/tunnel transport stays raw TCP reverse-connect only.

Result: the relay handles SNI-based routing and transparent raw TCP forwarding, preserving end-to-end TLS where applicable.

## Control Plane Flow

### 1. Register

- App/tunnel posts to `POST /sdk/register` with:
  - `lease_id`
  - `name`
  - `metadata`
  - `tls`
  - `reverse_token`
- Relay stores lease and (TLS only) registers SNI route.
- Route hostnames are generated from normalized lease + normalized `PORTAL_URL` host (extract host from URL without scheme/port/path); path segments are ignored, so `https://portal.example.com:8443/admin` and `https://portal.example.com` both map to `portal.example.com`.
- `/sdk/register` admission uses strict order: `IP -> Lease -> CertBind -> Token`.

### 2. Reverse Connect

- Backend opens a raw TCP reverse connection to `GET /sdk/connect` and streams traffic over that long-lived connection
  - `/sdk/connect` requires lease-bound client mTLS and applies strict admission order before hijacking:
    - `IP -> Lease -> CertBind -> Token`
  - Cert binding validates lease identity in client cert SAN/subject against request lease context.
- `X-Portal-Reverse-Token` is validated at HTTP precheck, then validated again in `ReverseHub` with centralized policy callbacks before the connection is pooled.
- Connection is pooled in `ReverseHub` only after token/IP checks pass.

### 3. Renew

- Backend sends `POST /sdk/renew` keepalive.
- `/sdk/renew` requires both lease-bound mTLS identity and `reverse_token`.
- Relay refreshes lease TTL and keeps route state current.

### 4. Unregister

- Backend sends `POST /sdk/unregister`.
- `/sdk/unregister` validates normalized `lease_id`, lease-bound mTLS identity, and token before deletion.
- Relay removes lease, route, and reverse pool.

## Admin Lease ID Contract

- `/admin/leases` returns plain lease IDs in `Peer`.
- `/admin/leases/banned` returns plain lease IDs (`[]string`).
- Base64URL encoding is used only in admin action path segments (`/admin/leases/{encodedLeaseID}/{action}`).

## Routing Behavior

`sni.Router` route lookup order:

1. Exact host match
2. Single-label wildcard (`*.example.com`)
3. No-route handler (used for exact portal-root host fallback to admin/API listener on the `PORTAL_URL` root host)

Note: wildcard does not match the portal root host itself (`example.com` or `portal.example.com`), so exact root-host matches trigger fallback to admin/API listener.
`PORTAL_URL` is normalized to its host component (scheme/port/path removed), so non-apex values such as `https://portal.example.com:8443/admin` become `<lease>.portal.example.com` and exact host matches still resolve through no-route fallback.

## Keyless and Certificates

- Relay keyless materials are stored in `KEYLESS_DIR`:
  - `fullchain.pem`
  - `privatekey.pem`
- If materials are missing and Cloudflare token is configured, relay can provision via ACME DNS-01.
- SDK/tunnel TLS mode uses keyless signer workflow and `/v1/sign` for remote signatures.

## Design Properties

- Reverse-only backend connectivity (no inbound port required on the app host)
- Per-lease reverse token authorization
- Separation of control plane (`/sdk/*`) and data plane (SNI + raw TCP forwarding)
- Single relay/tunnel transport policy: raw TCP reverse-connect only
- Mandatory control-plane identity policy: lease-bound mTLS with deterministic admission order
- Unified lease abstraction for routing, metadata, and lifecycle
- Shared anti-abuse path: admin-managed bans and lease authorization are enforced both in SDK registration and reverse admission

## Breaking-Change Upgrade Expectations

- This architecture wave is a hard-break for control-plane identity.
- Tunnels/SDK clients that do not present valid lease-bound mTLS identity are rejected deterministically.
- There is no token-only fallback mode after cutover.

## ADRs

- Decision records: [docs/adr/README.md](./adr/README.md)
