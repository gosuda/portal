# Glossary

This glossary defines key terms used in Portal.

## Portal / Relay

The central server that handles lease registration, routing, and reverse connection brokering.
It does not terminate end-to-end app payload in TLS passthrough mode.

## App (Service Publisher)

A service provider connected to Portal through Tunnel or Native SDK.
An app publishes one or more leases and serves traffic from local services.

## Client (Service Consumer)

The external user or browser that accesses a published service through the relay domain.

## Tunnel

The CLI-based publisher path (`cmd/portal-tunnel`).
It exposes existing local apps without code changes by forwarding relay traffic to a local host/port.

## Native SDK

The code integration path (`sdk/`) for Go applications.
It provides relay-backed listener APIs and metadata control for direct app integration.

## Lease

The routing and advertisement unit in Portal.
Each lease maps to one public endpoint and includes identity, name, metadata, TLS flag, and reverse token.

## Lease Name

The human-readable lease identifier used for subdomain routing (for example, `myapp` -> `myapp.example.com`).

## Reverse Token

A per-lease secret used to authenticate reverse connections (`/sdk/connect`) from backend to relay.

## ReverseHub

The relay-side pool of authenticated reverse connections, keyed by lease ID.
It provides connections for TLS SNI forwarding and HTTP proxy forwarding.

## SNI Router

The TCP router on relay SNI port (default `443`) that selects lease routes by TLS Server Name Indication (SNI).

## Keyless TLS

A mode where the backend handles TLS with remote signing support via relay signer endpoint (`/v1/sign`), without local private key distribution.

## ACME DNS-01

The certificate issuance/renewal method used with Cloudflare DNS API token when keyless materials are missing.

## Base Domain

The normalized host derived from `PORTAL_URL` and used to build service subdomains.
For non-apex values such as `https://portal.example.com:8443/admin`, the base domain is `portal.example.com` (scheme, path, and port are removed).

## Admin/API Server

The relay HTTP server (default `:4017`) that serves admin UI and SDK/control endpoints such as `/sdk/*`, `/admin`, and `/healthz`.
