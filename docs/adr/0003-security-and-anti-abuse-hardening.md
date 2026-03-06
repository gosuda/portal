# ADR 0003: Security and Anti-Abuse Hardening

- Status: `Deprecated`
- Date: `2026-03-03`
- Owners: `Portal maintainers`

## Context

Portal accepts unauthenticated internet traffic on relay/admin edges while managing long-lived reverse tunnel sessions. Abuse controls and security boundaries must be first-class or operational risk rises quickly.

This ADR captured a broader policy/bans/admin-settings model than the current runtime now implements.
Keep it as historical context only. Current descriptive docs and `AGENTS.md` are the source for the simplified runtime behavior until a replacement ADR is written.

## Decision

- Treat admin-authenticated controls (approval, settings, bans) as authoritative for runtime policy.
- Wire IP ban checks into SDK registration and reverse-connection acceptance paths.
- Enforce lease-token validation before bridging reverse connections.
- Keep root-domain and tenant-subdomain traffic split through SNI routing rules to prevent accidental cross-path handling.
- Standardize SDK endpoint handling: `/sdk/register` (and related SDK APIs) and `/sdk/connect` validation failures return JSON envelopes (`{ ok, error }`) with explicit error codes prior to connection hijack, and `/sdk/connect` remains subject to `ReverseHub` authorization before pooling.
- Use token-only admission on `/sdk/register`, `/sdk/connect`, `/sdk/renew`, and `/sdk/unregister` with deterministic order `IP -> Lease -> Token`.
- Do not request or validate client certificates for `/sdk/*` runtime admission; authorization is enforced by lease token and policy checks.
- Enforce installer binary integrity with mandatory SHA256 sidecar verification (`${BIN_URL}.sha256`) and fail-closed behavior on verification errors.

Operator setup remains unchanged: no new relay flags/env vars are introduced for anti-abuse behavior.

## Consequences

### Benefits

- Faster blocking response to abusive sources with centralized IP policy.
- Stronger boundary between control-plane actions and data-plane forwarding.
- Reduced chance of unauthorized reverse-connection use.

### Trade-offs

- Extra checks in critical paths may increase operational complexity during debugging.
- Incorrect ban-list management can block legitimate clients if policy operations are misused.
- SDK clients must classify `/sdk/connect` rejection codes and statuses into fatal vs retryable outcomes for stable reverse-worker behavior.

### Risks and Mitigations

- Risk: policy drift between admin state and runtime enforcement.
  Mitigation: initialize runtime components from admin-managed settings and keep a single IP manager source.
- Risk: abuse pressure shifts from one endpoint to another.
  Mitigation: enforce checks at multiple ingress points (SDK registration and reverse-hub admission).
- Risk: policy behavior drift from operator confusion.
  Mitigation: keep policy source single-owner (`admin` settings + IP manager) and document that operator bootstrap/setup remains stable.
- Risk: accidental weakening during refactors.
  Mitigation: require explicit ADR-aware review for security-sensitive path changes.

## Alternatives Considered

- Endpoint-local ad hoc checks only: rejected because policy diverges and creates inconsistent enforcement.
- Rely solely on external perimeter controls: rejected because application-level lease/auth context is required for accurate decisions.
