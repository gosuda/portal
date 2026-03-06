# AGENTS.md

## Purpose

This file is a high-signal rulebook for future agents.
Include only constraints that are expensive to rediscover from quick code search.

Source of truth for architecture decisions: `docs/adr/README.md` and linked ADRs.

## Non-Negotiable Architecture Invariants

1. **Raw TCP reverse-connect is the canonical transport.**
   - Why: ADR-0001 and ADR-0002 accepted this to keep NAT-friendly behavior and reduce protocol complexity.

2. **Do not introduce websocket or legacy compatibility paths unless a new ADR supersedes ADR-0002.**
   - Why: dual transport paths increase security and test surface and reintroduce drift.

3. **Derive routing hostnames from full portal root host in `PORTAL_URL` (supports non-apex), not apex extraction.**
   - Why: prevents SNI/public URL mismatches in non-apex deployments (ADR-0001).

4. **Keep explicit root-domain fallback behavior through SNI no-route handling to admin/API listener.**
   - Why: preserves intended control-plane vs tenant routing split (ADR-0001).

5. **All leases require TLS=true.** The register endpoint rejects `TLS=false`. The `RegisterRequest.TLS` field exists but non-TLS leases are not permitted.
   - Why: enforces end-to-end transport security for all tenant routes.

## Security and Anti-Abuse Invariants

1. **Admin-managed policy is authoritative for runtime security controls.**
   - Why: ADR-0003 requires central policy ownership to avoid endpoint-local drift.

2. **Do not rely on single-endpoint checks for abuse controls.**
   - Why: ADR-0003 expects enforcement across critical ingress paths (registration and reverse admission class paths).

3. **Reverse connection authorization must remain lease-token validated before bridge/forwarding.**
   - Why: prevents unauthorized tunnel attachment (ADR-0003).

4. **`/sdk/connect` must share the same policy source as `/sdk/register`.**
   - Why: registration and reverse admission must apply identical IP-ban + token checks in one enforcement pipeline.

## TLS and Identity Invariants

1. **Relay terminates admin/API TLS directly and also exposes `/v1/sign` as the keyless signer for tenant passthrough TLS.** The relay still does not terminate tenant traffic; it peeks ClientHello for SNI and bridges raw encrypted bytes, while the backend/tunnel endpoint performs the handshake using a `RemoteSigner`.
   - Why: preserves SNI passthrough data flow while centralizing certificate signing behind the relay keyless endpoint.

2. **/sdk/* control-plane auth is token.** Admission order is IP ban -> Lease -> Token.
   - Admin/API TLS listener does not request client certificates.
   - Keyless TLS (RemoteSigner for /v1/sign) remains independent and is still used for admin/API TLS termination.
   - Why: removes browser client-cert prompt side effects while keeping centralized token/IP policy enforcement.

3. **All relay URLs must be `https://`.** `NormalizeRelayAPIURL` rejects non-HTTPS. SDK and tunnel hard-fail on `http://`.
   - Why: enforces transport security without opt-out.

4. **`keyless_tls/` is published to `github.com/gosuda/keyless_tls`** and vendored as a directory for co-development. Root `go.mod` pins a specific pseudo-version or tag — no `replace` directive.
   - Why: enables external consumers while keeping co-development convenient. Pin version after pushing upstream changes.

## SNI Routing Invariants

1. **SNI wildcard matching is one-level only.** `sni.Router.GetRoute()` checks `*.parent.example.com` for `foo.parent.example.com` — not arbitrary depth.
   - Why: matches RFC TLS wildcard semantics.

2. **Protocol markers on reverse TCP connections:** `0x00` = keepalive, `0x01` = non-TLS start, `0x02` = TLS passthrough activation. The SNI router does true TLS passthrough — it peeks the ClientHello for routing, then bridges the raw encrypted connection without terminating TLS.
   - Why: binary protocol, not discoverable from HTTP-layer code.

3. **HTTP/2 is intentionally disabled on the admin HTTP server** (`TLSNextProto: make(…)`).
   - Why: the server hijacks connections for `/sdk/connect`; HTTP/2 multiplexing breaks hijack semantics.

## API Response Contract

1. **All HTTP responses use the `APIEnvelope` wrapper:** `{ ok: bool, data?: any, error?: { code, message } }` (defined in `types/api.go`). Write responses through `writeAPIData()`, `writeAPIOK()`, or `writeAPIError()` helpers — never raw JSON.
   - Why: cross-cutting contract across all endpoints; inconsistent envelopes break SDK and frontend parsing.

## Shared Types Package

1. **`types/` is reserved for shared wire/public types and protocol constants only.**
   - Allowed: request/response DTOs, shared metadata structs, protocol marker/header/path constants.
   - Not allowed: relay runtime state, broker/session state, server config, SDK lifecycle state, generic helpers.

2. **Do not import `portal` from `cmd/*` or `sdk` just to reach shared DTOs or protocol constants.**
   - Why: `portal` is relay runtime code; shared public shapes belong in `types/`.

## Operational Truths (CI-Aligned, Minimal)

1. **CI verification commands:** `make vet`, `make lint`, `make test`, `make vuln`. These are the enforced checks in `.github/workflows/ci.yml`. Note: `make tidy` is a local maintenance/pre-release step, not part of CI.

2. **`make build-server` does NOT call `make build-frontend`.** If called alone, `//go:embed dist/*` will be stale or empty. The Dockerfile calls both explicitly in order.
   - Why: prevents silent broken builds with missing frontend assets.

3. **`admin_settings.json` persists in the process CWD**, not in `KEYLESS_DIR`. State is lost on container restart unless CWD is a mounted volume.
   - Why: prevents state-loss surprises in production.

4. **`onLeaseDeleted` has dual registration.** `portal/relay.go` registers one callback; `cmd/relay-server/main.go` overwrites it with a broader one (adds IP/BPS cleanup). The outer callback supersedes.
   - Why: coupling hazard — modifying either registration without understanding both breaks cleanup.

## Change Discipline

1. If a code change violates any invariant above, update or add ADR and AGENTS in the same change set.
   - Why: keeps architecture docs and implementation synchronized.

2. Do not expand this file into repo summary, file tree guide, or generic handbook.
   - Why: high-noise AGENTS degrades future agent effectiveness.

## Go Conventions

**Imports:** stdlib → external → internal (blank-line separated), local prefix `github.com/gosuda`. **Concurrency:** `errgroup.Group` over `WaitGroup` · `errgroup.SetLimit` for bounded work · `context.WithTimeout` over `time.After` in loops (timer leak) · no bare `go func()` — creator owns lifecycle. **I/O:** `os.Root` (Go 1.24+) for directory-scoped file operations.

---

## Agent Behavior

**Verbalized sampling:** Before changes, sample 3–5 intent hypotheses (rank by likelihood, note one weakness each), assess coupling (structural/temporal/semantic), and tidy-first when coupling is high. Ask the human when trade-offs exist.

**Project rules:**
- No backward compatibility unless explicitly requested.
- No wrapper functions without demonstrated value.
- Consolidate changes in one pass — do not stack minimal patches.
- Run tests only when requested, before handoff, or for high-risk changes.
