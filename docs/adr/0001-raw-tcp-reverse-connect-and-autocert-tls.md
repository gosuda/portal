# ADR 0001: Raw TCP Reverse Connect and ACME TLS for Portal Root

- Status: `Accepted`
- Date: `2026-03-03`
- Owners: `Portal maintainers`

## Context

Portal must support NAT-friendly inbound connectivity for tenant traffic while keeping root-domain behavior predictable. The legacy approach mixed websocket assumptions into reverse-connect flow and derived TLS hosts inconsistently for non-apex portal domains.

## Decision

- Use raw TCP reverse-connect as the canonical transport between relay and tunnel clients.
- Keep SNI routing as the ingress split for tenant subdomains.
- Keep root-domain fallback forwarding from SNI router to the admin/API listener.
- Derive relay `BaseHost` and TLS domain construction from the full portal root host (for example `portal.example.com`), not apex extraction (`example.com`).
- Serve admin/API TLS from ACME-managed certificate material when files are available.

## Consequences

### Benefits

- Reverse-connect transport stays NAT-friendly and removes proxy protocol ambiguity.
- Root-domain behavior remains explicit: SNI fallback forwards to admin/API listener.
- SNI route registration, public URL derivation, and SDK TLS domain construction align on the same host derivation.

### Trade-offs

- TLS enablement still depends on ACME certificate files being present.
- Non-apex portal host deployments require wildcard coverage on the full portal root host (for example `*.portal.example.com`).

### Risks and Mitigations

- Risk: ACME certificate files unavailable at startup.
  Mitigation: keep HTTP fallback when ACME/root-host prerequisites are not met and log explicit TLS enablement state.
- Risk: non-apex portal host deployments route to wrong TLS host if derivation drifts.
  Mitigation: enforce portal-root-host derivation consistently in relay and SDK.

## Alternatives Considered

- Keep websocket reverse-connect transport for compatibility: rejected due to complexity and policy drift.
- Derive SNI routes from apex/base domain only: rejected due to `portal.example.com` mismatch failures.
