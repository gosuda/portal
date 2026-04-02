# Architecture

## Overview

Portal publishes local services on public subdomains and optional UDP ports through a relay.
Backends connect outward to the relay. Stream traffic is routed by SNI, and tenant TLS remains end-to-end between the client and the SDK or tunnel endpoint for the stream path.

High-level path:

```text
Stream client
  -> Relay SNI listener (:443 by default)
  -> Claimed reverse session
  -> SDK / portal-tunnel
  -> Local service

UDP client
  -> Relay lease UDP port (50000+ by default)
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
- Control-plane HTTP (`/sdk/*`), reverse-session establishment (`/sdk/connect`), and tenant TLS are separate connections with different trust boundaries.
- Relay does not terminate tenant TLS. It peeks ClientHello for SNI and bridges raw encrypted bytes after routing.
- SDK/tunnel endpoints terminate tenant TLS locally with a keyless-backed signer that calls the relay.
- In keyless TLS, the relay performs certificate private-key signing through `/v1/sign`, but the SDK/tunnel endpoint still runs the TLS server handshake and derives tenant TLS session keys locally.
- `/sdk/connect`, `/sdk/renew`, and `/sdk/unregister` are authorized by lease existence plus a relay-issued lease access token.
- `/sdk/register` is authenticated by a SIWE challenge/response flow using the SDK identity secp256k1 key. On success, the relay issues a lease-scoped ES256K JWT access token signed by the relay identity key and used for the rest of the lease lifecycle.
- Relay URLs must use `https://`.
- HTTP/2 stays disabled on the admin/API TLS listener because `/sdk/connect` depends on HTTP/1.1 hijacking semantics.
- WireGuard, when enabled, is relay-to-relay overlay transport only. It is not used for tenant stream TLS, public UDP ingress, or `/sdk/*` control-plane traffic.

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

- For non-localhost deployments, relay TLS can run from manual certificate files in `KEYLESS_DIR` or from managed ACME.
- When managed ACME is enabled, supported DNS providers are `cloudflare`, `gcloud`, and `route53`.
- ENS gasless automation reuses `ACME_DNS_PROVIDER` for DNSSEC and ENS TXT sync.
- Relay, tunnel, and demo-app identities are persisted as JSON at `IDENTITY_PATH` / `--identity-path`. Missing files are generated automatically and stored with `name`, `address`, `public_key`, and `private_key`.
- Managed non-localhost ACME keeps both root and wildcard DNS A records in sync.
- Relay certificate material lives under `KEYLESS_DIR` as `fullchain.pem` and `privatekey.pem`.
- Localhost uses the development certificate path instead of public managed/manual certificate setup.

## Connection Model

Portal has three distinct network roles:

- **Control-plane HTTP requests**
  - `POST /sdk/register/challenge`
  - `POST /sdk/register`
  - `POST /sdk/renew`
  - `POST /sdk/unregister`
  - `GET /sdk/domain`
- **Reverse session connection**
  - `GET /sdk/connect`
  - HTTP/1.1 only
  - hijacked into a long-lived raw TCP session
  - starts idle in the per-lease stream ready queue, then becomes the tenant data path when claimed
- **Internal datagram tunnel**
  - QUIC to the relay URL host:port with ALPN `portal-tunnel`
  - authenticated by a first-stream control message carrying `access_token`
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

- `Server`: owns listeners, lease registry, API handlers, discovery, and shutdown lifecycle
- `routeTable`: exact + single-label wildcard hostname lookup
- `transport.RelayStream`: per-lease ready queue for reverse stream sessions
- `transport.RelayDatagram`: per-lease raw UDP socket plus QUIC DATAGRAM bridge runtime
- `transport.PortAllocator`: count-based UDP port allocator with sticky name-based reservation and grace period
- `transport.datagramSession`: internal QUIC DATAGRAM bind/send/receive primitive shared by relay and SDK datagram runtimes
- `acme`: Cloudflare/Google Cloud DNS/Route53-backed root/wildcard A-record sync + certificate provisioning/renewal for the relay root host and wildcard
- `keyless`: admin/API TLS attach helpers and tenant-side signer integration
- `auth`: SIWE register challenge creation/verification plus lease access token issue/verify
- `discovery`: relay descriptor publication over relay HTTPS plus relay-set synchronization
- `wireguard`: optional relay overlay network used to reach peer relay APIs over internal overlay IPs and keep relay peer state synchronized
- `Server` additionally owns `quicTunnel` (QUIC listener, ALPN `portal-tunnel`) when UDP transport is enabled

### SDK (`sdk/`)

- `ExposeConfig.Discovery`: when true, `Expose` fetches the default Portal relay registry, merges it with explicit relay inputs, normalizes the result, and runs the relay discovery loop
- Entry points can opt out of registry defaults and call `utils.NormalizeRelayURLs` directly when they need explicit relay inputs only
- `Listener`: validates one relay URL locally, then starts relay compatibility checks, SIWE-based lease registration, reverse session maintenance, and lease renewal in the background until ready
- `Listener` owns a `transport.ClientStream` and, when UDP is enabled, a `transport.ClientDatagram`
- `api_client.go`: internal relay client for register challenge, register, renew, unregister, reverse session dialing, and QUIC tunnel setup
- `mitm.go`: tenant-side TLS passthrough self-probe. The SDK opens a probe connection to its own public URL, compares TLS exporter values on both SDK-controlled ends, and logs suspected relay-side TLS termination on mismatch; strict callers can opt into relay banning instead
- `ListenerConfig.RetryCount <= 0` means retry forever; positive values close the listener after the retry budget is exhausted
- `NewListener` callers provide explicit normalized relay URLs
- Default exposure flow is `Expose{Discovery: true} -> PublicURLs -> http.Server.Serve(exposure)`, with an opt-out path for explicit relay inputs only
- `expose.go`: optional `RunHTTP` helper for serving one handler on both a local HTTP port and the relay listener
- `Expose` keeps one listener per configured relay URL. Relay startup and reconnect failures are retried independently per relay, and successful relays remain available while failed relays keep retrying in the background
- `Exposure.RelayURLs()` returns the configured normalized relay URLs, while `Exposure.PublicURLs()` returns only relays that are currently registered and ready
- Relay-aware entry inspection is reserved for advanced callers such as `portal-tunnel`
- Tenant TLS is created automatically through the relay keyless signer; callers do not provide a local self-signed fallback path
- MITM self-probes are traffic-triggered, not periodic. A listener triggers at most one asynchronous probe per 30-second cooldown, and only after real tenant traffic performs I/O on an accepted connection
- Probe identification does not use a dedicated ALPN or fixed plaintext marker. The first encrypted probe payload is `nonce + random padding`, and inbound probe matching is only attempted while a probe is in flight
- `Listener.AcceptDatagram()` / `SendDatagram()`: read/write datagram frames via the client datagram runtime
- `Listener.DatagramReady()`: reports the published `udp_addr` plus whether the QUIC datagram plane is currently connected
- `Exposure.AcceptDatagram()`: receives datagrams from all backing relay listeners with relay context populated on `DatagramFrame`
- `Exposure.SendDatagram()`: sends a datagram frame back through the owning relay listener
- `Exposure.WaitDatagramReady()`: blocks until at least one relay listener has both a published `udp_addr` and a connected datagram plane

### Tunnel (`cmd/portal-tunnel`)

- Builds the `portal` CLI and exposes subcommands such as `portal expose` and `portal list`
- Loads or creates the local signing identity from `--identity-path` before starting the SDK exposure
- Creates one SDK listener per relay through the SDK and consumes one aggregate listener
- Accepts claimed tenant connections from the relay
- Proxies raw TCP to a local target passed to `portal expose`
- Optionally proxies raw UDP to a separate local UDP target when `--udp` is enabled
- Returns an HTTP 503 response when the local target is unavailable
- `--udp` flag (bool, default `false`): enables UDP relay in addition to TCP
- `--udp-addr` flag (string): local UDP target address (`host:port` or port only); required when `--udp` is enabled
- `--ban-mitm` flag (bool, default `false`): when enabled, TLS self-probe mismatches ban the relay for the current exposure instead of only logging
- `runUDPBestEffort`: waits for datagram readiness, then calls `proxyExposureDatagrams`
- `proxyExposureDatagrams` (`relays.go`): per-flow UDP sockets to local target with idle cleanup; uses `Exposure.SendDatagram()` for the return path
- Best-effort UDP failures are logged but do not terminate the TCP tunnel

## Transport Model

### Raw reverse transport (TLS only)

1. SDK/tunnel registers one lease per relay through `POST /sdk/register/challenge` followed by `POST /sdk/register`.
2. SDK opens one or more reverse sessions per registered lease with `GET /sdk/connect`.
3. Each relay hijacks `/sdk/connect` requests and places the connection in the per-lease stream ready queue.
4. While idle, the relay writes `0x00` keepalive markers.
5. A stream client connects to the relay SNI listener.
6. Relay extracts SNI from ClientHello, resolves a lease, and waits up to `ClaimTimeout` for one reverse session from that lease stream queue.
7. Relay writes `0x02` to activate the claimed session.
8. SDK/tunnel receives `0x02`, starts tenant TLS locally using the relay-backed keyless signer, and the relay bridges raw encrypted bytes end-to-end.

Result: the relay decides routing, but tenant TLS termination still happens at the SDK/tunnel side.

### Tenant TLS Self-Probe Detection

1. After a real tenant connection begins I/O, the SDK may start one asynchronous self-probe for that listener if no probe is in flight and the 30-second cooldown has expired.
2. The SDK opens a new TLS connection to its own public URL using the same tenant-facing TLS characteristics as normal traffic.
3. The probe client exports TLS keying material (`ExportKeyingMaterial`) from that probe connection and stores it under a random nonce.
4. The first encrypted probe payload is `16-byte nonce + random padding`; there is no fixed probe magic or dedicated ALPN.
5. When the probe connection comes back through the relay and reaches the SDK-side tenant TLS terminator, the SDK peeks only the first 16 encrypted application bytes while a probe is pending.
6. If those bytes match a pending nonce, the SDK exports keying material on the server side and compares it with the client-side exporter value.
7. Matching exporter values mean the probe observed passthrough for that connection. A mismatch is logged as suspected relay-side TLS termination. A timeout is logged as probe failure, not proof of MITM.

Result: this is a detect-only signal by default. It raises the cost of adaptive relay-side TLS termination, but it does not prove passthrough for every user connection. Callers that need stricter behavior can opt into relay banning.

### UDP/QUIC Datagram Transport

1. SDK/tunnel requests a register challenge with `udp_enabled=true`, signs the returned SIWE message, and then completes `POST /sdk/register`.
2. Relay validates that the datagram plane is enabled (server has `UDP_PORT_COUNT > 0` and admin has enabled UDP), allocates a UDP port via `PortAllocator`, and creates a `transport.RelayDatagram` for the lease.
3. Registration response includes `udp_addr` (public UDP endpoint) and `access_token`. There is no separate `quic_addr`; the SDK dials QUIC to the relay URL host:port.
4. SDK `transport.ClientDatagram` opens a QUIC connection with ALPN `portal-tunnel` and QUIC DATAGRAM support enabled.
5. Authentication: SDK sends `{access_token}` JSON on the first QUIC stream; the relay validates that lease access token before calling `RelayDatagram.Register(conn)`.
6. External UDP client sends a packet to `udp_addr` -> `RelayDatagram.readLoop` -> `TouchFlow` (assigns flow ID) -> `SendDatagram` -> QUIC DATAGRAM frame.
7. SDK-side `datagramSession.receiveLoop` decodes frames -> `Listener.AcceptDatagram()` -> `Exposure.AcceptDatagram()` -> `proxyExposureDatagrams` -> local UDP target.
8. Return path: local response -> `Exposure.SendDatagram()` -> `Listener.SendDatagram()` -> `ClientDatagram.Send()` -> QUIC DATAGRAM -> `RelayDatagram.dispatch()` -> `conn.WriteToUDP` to the original client.

```text
Client --UDP--> [:50000+ Relay] --DATAGRAM--> [RelayDatagram] --QUIC--> [ClientDatagram] --UDP--> Local Service
                                                          <--QUIC DATAGRAM return path--
```

Wire format (`types/transport.go`): `[flowID uvarint][payload bytes]`

## WireGuard Overlay and Discovery

- Discovery starts from bootstrap relay URLs over normal public HTTPS.
- Discovery descriptors are currently transport-authenticated by the queried relay endpoint, not by embedded descriptor signatures.
- Current discovery validation covers protocol version, descriptor normalization, required fields, expiry, target URL/identity matching, and overlay field sanity only.
- Descriptor `identity.address` is a relay claim inside discovery. Independent `domain -> address` verification comes from optional ENS/DNSSEC evidence, not from the discovery payload itself.
- Each relay publishes a descriptor over relay HTTPS that may advertise:
  - `wireguard_public_key`
  - `wireguard_endpoint`
  - `overlay_ipv4`
  - optional `overlay_cidrs`
- When discovery is enabled and the relay has a WireGuard private key, the relay creates an internal overlay interface and derives:
  - a relay WireGuard public key
  - an overlay IPv4 identity
  - a peer API listener bound on the overlay address
- The overlay peer API is plain HTTP on the WireGuard network, not public Internet HTTP. It serves the same discovery payload shape used by public `/discovery`.
- Bootstrap relays are discovered first over public HTTPS. Non-bootstrap relays that advertise overlay support become sync candidates and are polled again over the WireGuard overlay.
- Relay-set snapshots are translated into WireGuard peers with:
  - peer public key
  - endpoint
  - allowed IPs = peer overlay `/32` plus advertised overlay CIDRs
- Overlay failure affects inter-relay discovery and mesh synchronization only. Tenant stream routing, keyless TLS, register/renew/connect, and public UDP ingress do not depend on the WireGuard transport path directly.

## Control Plane Flow

### 1. Register

- `POST /sdk/register/challenge`
- `POST /sdk/register`
- JSON envelope response
- Challenge request fields:
  - `identity`
  - `metadata`
  - `ttl`
  - `udp_enabled`
- Challenge response fields:
  - `challenge_id`
  - `expires_at`
  - `siwe_message`
- Caller signs the returned SIWE message with the identity Ethereum private key (`personal_sign`) and then submits:
  - `challenge_id`
  - `siwe_message`
  - `siwe_signature`
- `name` must be a valid single DNS label and relay publishes the lease at `<name>.<root host>`
- Registration reserves the hostname and publishes the route immediately; if no reverse session is ready yet, inbound SNI claims wait up to `ClaimTimeout`
- When registration succeeds, the response includes:
  - `identity`
  - `hostname`
  - `expires_at`
  - `access_token`
  - optional `udp_addr`
- `access_token` is a relay-issued ES256K JWT signed by the relay identity key and validated with:
  - `iss = PORTAL_URL`
  - `aud = portal-sdk`
  - `sub = identity.key()`
  - `identity`
  - `iat`, `nbf`, `exp`
  - `jti`
- UDP registration requires two conditions: server must have `UDP_PORT_COUNT > 0` and admin must enable UDP in the admin panel
- `APIErrorCodeUDPDisabled` (HTTP 403) when UDP is disabled by admin policy
- `APIErrorCodeUDPCapacityExceeded` (HTTP 503) when the admin-configured max UDP lease limit is reached
- `APIErrorCodeUDPPortExhausted` (HTTP 503) when the UDP port pool is exhausted
- `PORTAL_URL` is normalized to its host component only; path/query segments are ignored for routing

### 2. Reverse Connect

- `GET /sdk/connect`
- Requires HTTP/1.1
- Requires `X-Portal-Access-Token` header with the lease access token
- Relay validates:
  - lease exists and is not expired
  - the lease access token signature, issuer, audience, identity, and expiry are valid
- After claim, relay writes `0x02` before switching the session into tenant TLS passthrough
- After hijack, the connection becomes a broker-managed reverse session

### 3. Renew

- `POST /sdk/renew`
- Requires `access_token`
- Extends lease TTL and returns a refreshed `access_token`

### 4. Unregister

- `POST /sdk/unregister`
- Requires `access_token`
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
- `types/error.go`
  - shared API error codes
  - shared MITM self-probe reason codes
- `types/types.go`
  - shared headers
  - reverse marker constants
- `types/paths.go`
  - shared `/sdk/*`, admin, health, install, and signer paths
- `types/transport.go`
  - `ErrDatagramTooSmall`
  - `DatagramFrame` wire frame plus SDK relay context
  - `EncodeDatagram` / `DecodeDatagram`

Relay-local frontend asset filenames stay in `cmd/relay-server`, not `types/`.

## Keyless and Certificates

- Relay admin/API TLS uses the certificate in `KEYLESS_DIR`
  - `fullchain.pem`
  - `privatekey.pem`
- For non-localhost deployments, Portal can either use those files directly or manage them through ACME.
- When ACME is enabled, DNS-01 currently supports `cloudflare`, `gcloud`, and `route53`, and keeps:
  - root host A record
  - wildcard host A record
  - relay certificate renewal
- When manual certificate files are present and valid, Portal uses them instead of provisioning a new certificate, but can still use `ACME_DNS_PROVIDER` for ENS gasless DNSSEC/TXT automation.
- SDK/tunnel fetches the relay certificate chain from the relay root host, verifies that the leaf covers tenant hostnames, and builds a tenant-side `tls.Config` with a remote signer backed by `/v1/sign`
- During tenant TLS handshake, the SDK/tunnel endpoint acts as the TLS server and derives tenant session keys locally; the relay only signs handshake digests and does not receive tenant TLS traffic secrets
- Relay control-plane TLS and reverse-session setup still terminate on the relay's admin/API listener and are not protected by the tenant keyless TLS path

## Design Properties

- Reverse-only backend connectivity
- One canonical raw TCP reverse transport
- Raw public UDP exposure with an internal QUIC datagram backhaul
- Optional WireGuard relay overlay for relay discovery and peer synchronization
- SNI-based routing with root-host fallback
- End-to-end tenant TLS with relay-backed keyless signing
- Traffic-triggered detect-only MITM self-probing for probable relay-side TLS termination
- SIWE identity proof for registration plus relay-issued ES256K JWT access tokens for the lease lifecycle
- Lease-local stream and datagram ownership through per-lease transport runtimes
- Optional QUIC/UDP datagram transport coexisting with TCP on the same lease
- Per-lease UDP port allocation with sticky name-based reservation
- QUIC tunnel authentication via control stream (`access_token`)

## ADRs

- Decision records: [docs/adr/README.md](./adr/README.md)
