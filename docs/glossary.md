# Glossary

Key terms used in Portal.

## Portal / Relay

The central server that handles lease registration, SNI routing, reverse-session brokering, admin/API TLS, and keyless signing.
It does not terminate tenant TLS.

## App (Service Publisher)

A backend service connected to Portal through the `portal` CLI (`cmd/portal-tunnel`) or the native Go SDK.
An app publishes one or more leases and serves traffic from a local process.

## Client (Service Consumer)

A browser or external caller that accesses a published service through relay-managed hostnames.

## Control-Plane Request

An ordinary HTTPS request to the relay admin/API listener, such as:

- `/sdk/register`
- `/sdk/renew`
- `/sdk/unregister`
- `/sdk/domain`

These use the JSON API envelope contract.

## Reverse Session

A long-lived raw TCP connection opened through `GET /sdk/connect?lease_id=...`.
The relay hijacks it, keeps it idle in a lease broker, and later claims it for one tenant TLS passthrough connection.

## Lease

Portal's routing and advertisement unit.
Each lease has an ID, display name, hostnames, metadata, expiry, reverse token, and a broker of ready reverse sessions.

## Lease Name

The canonical single DNS label for a lease. Portal publishes the lease at `<name>.<root host>` and also uses the same value for admin/UI display.

## Lease Broker

The relay-side owner of reverse session state for one lease.
It manages the ready queue, claim/wakeup behavior, drop/stop lifecycle, and idle keepalive policy.

## Reverse Token

A per-lease secret supplied at registration time and later required by:

- `/sdk/connect`
- `/sdk/renew`
- `/sdk/unregister`

It authorizes reverse-session and lease-lifecycle operations.

## Route Table

The relay hostname map that resolves exact and single-label wildcard matches to a lease ID.

## SNI Routing

The relay reads ClientHello to extract the requested hostname, chooses a lease route, and then bridges the original encrypted TLS stream without terminating it.

## Keyless TLS

A mode where the SDK/tunnel terminates tenant TLS locally while delegating private-key signing to the relay `/v1/sign` endpoint.
This keeps the relay out of the tenant data plane while avoiding direct private-key distribution to every backend host.

## ACME DNS-01

The relay certificate issuance and renewal path for non-localhost deployments.
It currently supports `cloudflare` and `route53` to provision the root and wildcard certificate coverage used by the relay.

## Base Domain / Root Host

The host extracted from `PORTAL_URL` after removing scheme, port, path, query, and fragment.
For `https://portal.example.com:8443/admin`, the root host is `portal.example.com`.

## Admin/API Server

The relay HTTPS listener (default `:4017`) serving:

- `/sdk/*`
- `/admin`
- `/admin/snapshot`
- `/admin/leases/*`
- `/healthz`
- `/v1/sign`
- frontend root/app routes through root-host fallback
