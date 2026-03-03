# Glossary

Key terms used in Portal.

## Portal / Relay

The central server that handles lease registration, routing, and reverse-connection brokering.
In TLS passthrough mode, it routes transport and does not terminate app payload TLS.
All backend-to-relay ingress uses a long-lived raw TCP reverse-connect channel (`/sdk/connect`); websocket compatibility transport is not supported.

## App (Service Publisher)

A backend service connected to Portal through Tunnel or Native SDK.
An app publishes one or more leases and serves traffic from local services.

## Client (Service Consumer)

A browser or external caller that accesses a published service through relay-managed domains.

## Tunnel

The CLI publisher path (`cmd/portal-tunnel`).
It forwards relay traffic to an existing local host/port without app code changes.

## Native SDK

The Go integration path (`sdk/`).
It provides relay-backed listener APIs and lease metadata control for direct integration.

## Lease

Portal's routing and advertisement unit.
Each lease maps to one public endpoint and includes identity, name, metadata, TLS flag, and reverse token.

## Lease Name

The human-readable identifier used for subdomain routing (for example, `myapp` -> `myapp.example.com`).

## Reverse Token

A per-lease secret used to authenticate reverse connections (`/sdk/connect`) from backend to relay.

## ReverseHub

Relay-side pool of authenticated reverse connections keyed by lease ID.
It supplies raw TCP reverse connections for TLS SNI forwarding.

## SNI Router

The TCP router on relay SNI port (default `443`) that selects lease routes by TLS SNI.
Exact matches on the portal root host (derived from `PORTAL_URL` host) are intentionally routed via no-route fallback to the admin/API listener.

## Keyless TLS

A mode where the backend performs TLS while using the relay signer endpoint (`/v1/sign`) for remote signing.
This avoids distributing private keys to every backend host.

## ACME DNS-01

Certificate issuance/renewal method used with a Cloudflare DNS API token when keyless materials are missing.

## Base Domain

The host extracted from `PORTAL_URL` (scheme, port, and path removed) and used to build service subdomains.
For non-apex values such as `https://portal.example.com:8443/admin`, the base host is `portal.example.com`.
The same host is used for exact-match SNI fallback, which routes root-host requests to the admin/API listener.

## Admin/API Server

The relay HTTP server (default `:4017`) serving admin UI and control endpoints such as `/sdk/*`, `/admin`, and `/healthz`.
It also receives root-domain fallback traffic from SNI when no more specific lease route is found.
