# AGENTS.md

Repo-specific guidance for automated agents working on Portal.

## Quick Commands

Build:
- `make build` (all artifacts)
- `make build-server` (relay server binary)
- `make build-frontend` (React admin UI)
- `make build-tunnel` (portal-tunnel binaries)

Run:
- `make run` (run `./bin/relay-server`)
- `docker compose up` (full stack, relay at :4017, admin at `/admin`)

Lint/Format/Test:
- `make fmt` (gofmt + goimports)
- `make vet` (go vet)
- `make lint` (golangci-lint)
- `make test` (go test -v -race ./...)
- `make vuln` (govulncheck)
- `make tidy` (go mod tidy + go mod verify)

Single test:
- `go test -v -run TestName ./path/to/pkg`

Frontend dev:
- `cd cmd/relay-server/frontend && npm run dev`
- `cd cmd/relay-server/frontend && npm run build`
- `cd cmd/relay-server/frontend && npm run lint`

## Architecture (Big Picture)

Portal is a relay network that connects Apps (service publishers) and Clients (service consumers) through a central relay server using TLS SNI passthrough — the relay routes by hostname without decrypting payloads.

Core components:
- Relay server: `cmd/relay-server` (HTTP API, admin UI serving, SNI routing)
- Relay core logic: `portal/` (lease manager, reverse connection hub)
- Funnel SNI passthrough: `portal/utils/sni/` (TLS ClientHello parsing, SNI routing, connection bridging)
- Reverse connection hub: `portal/reverse_hub.go` (pre-established WebSocket pool for funnel mode)
- Funnel REST API: `cmd/relay-server/registry.go` (lease registration, renewal, unregistration)
- Shared API types: `portal/api/` (request/response types for funnel REST API)
- SDK for Apps: `sdk/` (FunnelClient + FunnelListener)
- Tunnel client: `cmd/portal-tunnel/` (exposes local services via funnel)
- Admin frontend: `cmd/relay-server/frontend/` (built into `cmd/relay-server/dist/app`)

## Connection Flow (High Level)

1. App (tunnel client) registers a Funnel Lease via REST API (`POST /api/register`).
2. App opens pre-established reverse WebSocket connections to the relay (`GET /api/connect`).
3. Browser connects to `<name>.<funnel-domain>:443` — the SNI router extracts the hostname.
4. Relay matches the SNI to a lease, acquires a reverse connection from the hub, and signals the tunnel.
5. Tunnel performs TLS termination locally and proxies to the local service.
6. Data flows bidirectionally: browser ↔ relay (SNI passthrough) ↔ tunnel ↔ local service.

## Key Terms

- Portal / Relay: central mediator; passes TLS traffic through without decryption.
- App / Tunnel: service publisher using SDK (`sdk/`) or portal-tunnel binary to register leases.
- Client: consumer (typically a browser) connecting via funnel subdomain.
- Lease: advertising unit; one Lease maps to one public HTTPS endpoint.
- Funnel: TLS SNI passthrough mode where the relay routes by hostname.
- Reverse Connection: pre-established WebSocket from tunnel to relay, pooled in ReverseHub.

## Where to Look

- `cmd/relay-server/` (entrypoint, HTTP API, admin UI)
- `cmd/relay-server/registry.go` (funnel REST API handlers)
- `portal/` (lease manager, reverse connection hub)
- `portal/api/` (shared API request/response types)
- `portal/utils/sni/` (SNI parser, router, bridge)
- `portal/reverse_hub.go` (reverse connection management)
- `sdk/` (FunnelClient + FunnelListener)
- `cmd/portal-tunnel/` (tunnel client)

## Repo Basics

- Module: `gosuda.org/portal`
- Go version: 1.25.3 (from `go.mod`)

## Gosuda Go Standards

Formatting & style:
- Run formatting before commits (see Quick Commands).
- Import order: stdlib -> external -> internal (blank-line separated).
- Naming: packages lowercase single-word; interfaces as behavior verbs; errors use `Err` prefix for sentinels and `Error` suffix for types.
- Context first parameter for public I/O: `func Do(ctx context.Context, ...)`.
- CGo disabled: `CGO_ENABLED=0`.

Static analysis & linters:
- Use `go vet`, `golangci-lint`, `go test -race`, and `govulncheck` (see Quick Commands).
- Linter tiers: correctness, quality, concurrency safety, and performance/modernization (configured in `.golangci.yml`).

Error handling:
- Wrap with `%w` and include call-site context.
- Sentinel errors per package; use `errors.Is`/`errors.As`.
- Use `errors.Join` for multi-error.
- Never ignore errors unless explicitly excluded by errcheck.

Iterators (Go 1.23+):
- Signatures: `func(yield func() bool)`, `func(yield func(V) bool)`, `func(yield func(K, V) bool)`.
- Always check yield return; prefer stdlib helpers like `slices.Collect` and `maps.Keys`.

Context & concurrency:
- Prefer `errgroup.Group` for parallel work, `SetLimit` for bounds.
- No goroutines without clear exit; creator owns lifecycle.
- Directional channels in signatures; only sender closes.
- Avoid `time.After` in loops; use `context.WithTimeout` or `time.Ticker`.

Testing:
- Use race detector in normal test runs.
- Use `t.Context()` in tests where applicable.
- Benchmarks should use `for b.Loop() {}`.

Security:
- Use `govulncheck` and `go mod verify` during release workflows.
- Avoid `math/rand` for security-sensitive operations.

Performance:
- Avoid `reflect` on hot paths; prefer generics or type switches.
- Use `sync.Pool` for hot paths only.

Module hygiene:
- Always commit `go.mod` and `go.sum`; never commit `go.work`.
- Pin toolchain version to match `go.mod` (currently 1.25.3).

CI/CD:
- CI runs test -> lint -> security -> build (`.github/workflows/ci.yml`).

Verbalized sampling:
- For non-trivial changes: sample multiple intents, explore edge cases, assess coupling, tidy first, and surface tradeoffs.

## AI Quality System

Portal has a 4-system AI quality framework in `.ai/`:
- **Manuals**: Domain-specific rules in `.ai/manuals/{relay-server,protocol,sdk,frontend}/`
- **Memory**: Persistent task state in `.ai/memory/` (3-document pattern: PLAN/CONTEXT/CHECKLIST)
- **Quality gate**: `bash .ai/scripts/verify.sh` (runs vet/lint/test/vuln/build)
- **Agent teams**: Specialized review prompts in `.ai/agents/`

See `AI-RULES.md` for the full operating system. See `.ai/config.md` for manual matching rules.

### Completion Protocol (MANDATORY — before declaring work done)

**Every non-trivial task MUST complete these steps before marking as done:**

1. **Quality gate**: Run `bash .ai/scripts/verify.sh` — all checks must pass (or failures explained)
2. **Security review**: Apply `.ai/agents/quality-team.md` checklist to all changed files
3. **Report**: Save findings to `.ai/reports/quality/[task]-[date].md` using `.ai/templates/REPORT-template.md`

**Team deployment by scale:**
- 1-2 files changed → Quality review only
- 3-5 files changed → Quality + Test review
- 5+ files changed → Quality + Test + Planning review

### Task Memory (MANDATORY — always active)

**Every non-trivial task MUST use the 3-document memory system.** This is persistent state that survives across sessions.

**Session start procedure:**
1. Read `.ai/memory/DASHBOARD.md` to check for active/completed tasks
2. If resuming an active task, read its `CONTEXT.md` and `CHECKLIST.md` in `.ai/memory/active/[task-name]/`
3. If starting a new task, create 3 docs from templates in `.ai/templates/`

**3-Document Pattern:**

| Document | File | Purpose |
|----------|------|---------|
| Plan | `PLAN.md` | What to build, scope, steps |
| Context | `CONTEXT.md` | Why decisions were made, references, caveats |
| Checklist | `CHECKLIST.md` | Progress tracking, completion criteria |

**Storage:** `.ai/memory/active/[task-name-kebab-case]/`

**When to update:**

| Event | Update |
|-------|--------|
| Checklist item done | CHECKLIST.md |
| Technical decision | CONTEXT.md — decision log |
| Unexpected finding | CONTEXT.md — caveats |
| Issue discovered | CHECKLIST.md — issue tracker |
| Session end | All 3 docs + DASHBOARD.md |

**Task end:** Verify all CHECKLIST criteria met → move `active/` → `completed/` → update DASHBOARD.md

**Integration:** `TodoWrite` tracks in-session progress (ephemeral). 3-document system tracks cross-session state (persistent). Both are used simultaneously.
