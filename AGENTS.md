# AGENTS.md

## Purpose

This file is a high-signal rulebook for future agents.
Include only constraints that are expensive to rediscover from quick code search.

Source of truth for architecture decisions: `docs/adr/README.md` and linked ADRs.
Descriptive docs under `docs/` should match current code paths.

## Non-Negotiable Architecture Invariants

1. **Raw TCP reverse-connect is the canonical transport.**
   - Why: keeps backend connectivity NAT-friendly and avoids parallel transport stacks.

2. **Do not introduce websocket or legacy compatibility paths unless a new ADR supersedes ADR-0002.**
   - Why: dual transport paths increase security and test surface and reintroduce drift.

3. **Derive routing hostnames from the full portal root host in `PORTAL_URL` (supports non-apex), not apex extraction.**
   - Why: prevents SNI/public URL mismatches in non-apex deployments.

4. **Keep explicit root-domain fallback behavior through SNI no-route handling to the admin/API listener.**
   - Why: preserves the intended split between root-host control-plane traffic and tenant subdomain traffic.

5. **All leases are TLS-only.** The register endpoint does not accept a non-TLS mode.
   - Why: all tenant routes are expected to stay on the TLS passthrough path.

## TLS and Identity Invariants

1. **Relay terminates admin/API TLS on the root host and also exposes `/v1/sign` for tenant-side keyless signing.**
   - Relay still does not terminate tenant TLS. It peeks ClientHello for SNI and bridges raw encrypted bytes.
   - SDK/tunnel endpoints terminate tenant TLS locally with a keyless-backed signer that calls the relay.

2. **`/sdk/connect`, `/sdk/renew`, and `/sdk/unregister` are authorized by lease existence plus reverse token.**
   - `/sdk/register` requires the caller to provide a reverse token for later use, but registration itself is not separately authenticated by that token.

3. **All relay URLs must be `https://`.**
   - Why: SDK and tunnel are expected to hard-fail on insecure relay URLs.

4. **HTTP/2 is intentionally disabled on the admin/API TLS listener.**
   - Why: `/sdk/connect` depends on HTTP/1.1 hijacking semantics.

## Reverse Session Protocol

1. **SNI wildcard matching is one-level only.**
   - `*.parent.example.com` matches `foo.parent.example.com`, not arbitrary depth.

2. **Protocol markers on reverse TCP connections are still meaningful protocol state.**
   - `0x00` = idle keepalive
   - `0x02` = TLS passthrough activation
   - Why: SDK waits on these bytes before switching an idle reverse session into a claimed tenant TLS session.

3. **`/sdk/connect` must remain HTTP/1.1 only.**
   - Why: hijacking and long-lived reverse sessions depend on HTTP/1.1 connection ownership.

## API Response Contract

1. **All JSON control-plane responses use the `APIEnvelope` wrapper:** `{ ok: bool, data?: any, error?: { code, message } }` (defined in `types/api.go`).
   - Write responses through `writeAPIData()`, `writeAPIOK()`, or `writeAPIError()`.
   - Admin HTML pages, tunnel script/binary responses, and other non-JSON endpoints are exceptions.

## Shared Types Package

1. **`types/` is reserved for shared wire/public types and cross-package constants only.**
   - Allowed: request/response DTOs, shared metadata structs, protocol markers, shared headers, shared public path constants.
   - Not allowed: relay runtime state, broker/session state, server config, SDK lifecycle state, generic helpers.

2. **Shared control-plane and public route constants that cross package boundaries belong in `types/paths.go`.**
   - Examples: `/sdk/*`, `/v1/sign`, `/healthz`, `/admin`, `/admin/leases`, `/tunnel`.

3. **Relay-local frontend asset paths stay local to `cmd/relay-server`.**
   - Why: filenames like `favicon.svg` or `portal.jpg` are frontend serving details, not cross-package API contract.

4. **Do not import `portal` from `cmd/*` or `sdk` just to reach shared DTOs or constants.**
   - Why: `portal` is relay runtime code; shared public shapes belong in `types/`.

## Operational Truths (CI-Aligned, Minimal)

1. **CI verification commands:** `make vet`, `make lint`, `make test`, `make vuln`.
   - `make tidy` is a local maintenance step, not part of CI.

2. **`make build-server` does not build the frontend first.**
   - Why: `cmd/relay-server/dist/*` is embed input; build the frontend explicitly before packaging the relay binary.

3. **ACME management supports only `cloudflare` and `route53`, and keeps both root and wildcard DNS A records in sync for non-localhost deployments.**
   - Certificates and keys live under `KEYLESS_DIR` as `fullchain.pem` and `privatekey.pem`.
   - Localhost uses the development certificate path instead of DNS-provider-managed ACME.

## Change Discipline

1. If a code change violates any invariant above, update or add ADR and AGENTS in the same change set.
   - Why: keeps architecture docs and implementation synchronized.

2. Do not expand this file into repo summary, file tree guide, or generic handbook.
   - Why: high-noise AGENTS degrades future agent effectiveness.

## Go Conventions

**Imports:** stdlib -> external -> internal (blank-line separated), local prefix `github.com/gosuda/portal/v2`.  
**Concurrency:** `errgroup.Group` over `WaitGroup`; `errgroup.SetLimit` for bounded work; `context.WithTimeout` over `time.After` in loops; no bare `go func()` without owned lifecycle.  
**I/O:** prefer directory-scoped operations when touching filesystem trees.

---

## Agent Behavior

**Verbalized sampling:** Before changes, sample 3-5 intent hypotheses (rank by likelihood, note one weakness each), assess coupling (structural/temporal/semantic), and tidy-first when coupling is high. Ask the human when trade-offs exist.

**Project rules:**
- No backward compatibility unless explicitly requested.
- No wrapper functions without demonstrated value.
- Consolidate changes in one pass; do not stack minimal patches.
- Run tests only when requested, before handoff, or for high-risk changes.
