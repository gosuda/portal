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
   - Why: ensures registration and reverse admission apply identical IP-ban + token checks in one enforcement pipeline.

5. **Operator setup is not changed by this hardening.**
   - Why: anti-abuse changes are behavior-only and reuse existing flags/env/settings for policy management.

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

3. **Assume Go toolchain baseline from `go.mod` (currently 1.26.x).**
   - Why: CI resolves Go from `go.mod`; avoid stale version assumptions.

4. **Use `Makefile` as build and verification authority; do not reference absent tooling (for example, no `justfile` in this repo).**
   - Why: reduces operational drift and broken command guidance.

## Change Discipline

1. If a code change violates any invariant above, update or add ADR and AGENTS in the same change set.
   - Why: keeps architecture docs and implementation synchronized.

2. Do not expand this file into repo summary, file tree guide, or generic handbook.
   - Why: high-noise AGENTS degrades future agent effectiveness.

## Go Conventions

**Format:** `gofmt -w . && goimports -w .` before every commit.

**Imports:** stdlib → external → internal (blank-line separated). Local prefix: `github.com/gosuda`.

**CGo:** always disabled — `CGO_ENABLED=0`. Pure Go only.

**Module:** commit `go.mod`+`go.sum`, never `go.work` · pin toolchain in `go.mod` · `go mod tidy && go mod verify && govulncheck ./...` pre-release · `os.Root` (Go 1.24+) for directory-scoped I/O.

**Concurrency:** `errgroup.Group` over `WaitGroup` · `errgroup.SetLimit` for bounded work · `context.WithTimeout` over `time.After` in loops (timer leak) · no bare `go func()` — creator owns lifecycle.

---

## Verbalized Sampling

Before trivial or non-trivial changes, AI agents **must**:

1. **Sample 3–5 intent hypotheses** — rank by likelihood, note one weakness each
2. **Explore edge cases** — at least 3 standard, 5 for architectural changes
3. **Assess coupling** — structural (imports), temporal (co-changing files), semantic (shared concepts)
4. **Tidy first** — high coupling → extract/split/rename before changing; low → change directly
5. **Surface decisions** — ask the human when trade-offs exist; do exactly what is asked, no more

## Project-specific rules [**ENFORCED**]

- Do not keep backward compatibility unless explicitly requested.
- Do not add meaningless wrapper functions unless they provide demonstrated value.
- Do not stack minimal patches that fragment logic — complete consolidation in one change.
- Do not run tests on every execution — only when requested, before handoff, or for high-risk changes.
