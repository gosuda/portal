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

1. **Relay holds the TLS private key; SDK/tunnel never does.** SDK calls `/v1/sign` on the relay via `RemoteSigner` for all private key operations.
   - Why: prevents key material leakage to untrusted tunnel endpoints.

2. **mTLS is implicit (optional) for `/sdk/*` control-plane paths.** When a client cert is presented, the relay validates it (CertBind stage). When absent, CertBind is skipped and token auth alone is used.
   - `KEYLESS_DIR` env var presence triggers SDK lifecycle identity issuance and client cert presentation. When unset, the SDK operates in token-only mode.
   - Keyless TLS (`RemoteSigner` for `/v1/sign`) is independent of mTLS — always used for TLS termination regardless of client cert presence.
   - Why: ADR-0003 admission order is IP ban → Lease → [CertBind if cert present] → Token. Invalid certs are still rejected; absent certs skip CertBind.

3. **All relay URLs must be `https://`.** `NormalizeRelayAPIURL` rejects non-HTTPS. SDK and tunnel hard-fail on `http://`.
   - Why: enforces transport security without opt-out.

4. **`keyless_tls/` is published to `github.com/gosuda/keyless_tls`** and vendored as a directory for co-development. Root `go.mod` pins a specific pseudo-version or tag — no `replace` directive.
   - Why: enables external consumers while keeping co-development convenient. Pin version after pushing upstream changes.

## SNI Routing Invariants

1. **SNI wildcard matching is one-level only.** `sni.Router.GetRoute()` checks `*.parent.example.com` for `foo.parent.example.com` — not arbitrary depth.
   - Why: matches RFC TLS wildcard semantics.

2. **Protocol markers on reverse TCP connections:** `0x00` = keepalive, `0x02` = TLS passthrough activation.
   - Why: binary protocol, not discoverable from HTTP-layer code.

3. **HTTP/2 is intentionally disabled on the admin HTTP server** (`TLSNextProto: make(…)`).
   - Why: the server hijacks connections for `/sdk/connect`; HTTP/2 multiplexing breaks hijack semantics.

## Operational Truths (CI-Aligned, Minimal)

1. **Local lint workflow: run `make lint-auto` first, then `make lint`.**
   - Why: `lint-auto` applies safe rewrites locally, while `lint` is the strict non-mutating gate that matches CI.

2. **Use CI-equivalent verification when validating high-risk changes:**
   - `make vet`
   - `make lint`
   - `make test`
   - `make vuln`
   - Why: these are the enforced checks in `.github/workflows/ci.yml`.
   - Note: `make tidy` is a local maintenance/pre-release step and is not currently part of the CI workflow.

3. **Assume Go toolchain baseline from `go.mod`.**
   - Why: CI resolves Go from `go.mod`; avoid stale version assumptions.

4. **Use `Makefile` as build and verification authority; do not reference absent tooling (for example, no `justfile` in this repo).**
   - Why: reduces operational drift and broken command guidance.

5. **`make build-server` does NOT call `make build-frontend`.** If called alone, `//go:embed dist/*` will be stale or empty. The Dockerfile calls both explicitly in order.
   - Why: prevents silent broken builds with missing frontend assets.

6. **`admin_settings.json` persists in the process CWD**, not in `KEYLESS_DIR`. State is lost on container restart unless CWD is a mounted volume.
   - Why: prevents state-loss surprises in production.

7. **`onLeaseDeleted` has dual registration.** `portal/relay.go` registers one callback; `cmd/relay-server/main.go` overwrites it with a broader one (adds IP/BPS cleanup). The outer callback supersedes.
   - Why: coupling hazard — modifying either registration without understanding both breaks cleanup.

## Change Discipline

1. If a code change violates any invariant above, update or add ADR and AGENTS in the same change set.
   - Why: keeps architecture docs and implementation synchronized.

2. Do not expand this file into repo summary, file tree guide, or generic handbook.
   - Why: high-noise AGENTS degrades future agent effectiveness.

## Go Conventions

**Format:** `gofmt -w . && goimports -w .` before every commit. Imports: stdlib → external → internal (blank-line separated), local prefix `github.com/gosuda`.

**CGo:** always disabled — `CGO_ENABLED=0`. Pure Go only.

**Module:** commit `go.mod`+`go.sum`, never `go.work` · pin toolchain in `go.mod` · `go mod tidy && go mod verify && govulncheck ./...` pre-release · `os.Root` (Go 1.24+) for directory-scoped I/O.

**Concurrency:** `errgroup.Group` over `WaitGroup` · `errgroup.SetLimit` for bounded work · `context.WithTimeout` over `time.After` in loops (timer leak) · no bare `go func()` — creator owns lifecycle.

---

## Agent Behavior

**Verbalized sampling:** Before changes, sample 3–5 intent hypotheses (rank by likelihood, note one weakness each), assess coupling (structural/temporal/semantic), and tidy-first when coupling is high. Ask the human when trade-offs exist.

**Project rules:**
- No backward compatibility unless explicitly requested.
- No wrapper functions without demonstrated value.
- Consolidate changes in one pass — do not stack minimal patches.
- Run tests only when requested, before handoff, or for high-risk changes.
