# AGENTS.md

Repo-specific guidance for automated agents working on Portal.

## Quick Commands

Build:
- `make build` (all artifacts)
- `make build-server` (relay server binary)
- `make build-frontend` (React admin UI)
- `make build-wasm` (webclient WASM)
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

Portal is a relay network that connects Apps (service publishers) and Clients (service consumers) through a central relay server without decrypting payloads.

Core components:
- Relay server: `cmd/relay-server` (HTTP + WS relay, admin UI serving)
- Relay core logic: `portal/` (lease manager, connection handlers, forwarding)
- Crypto + protocols: `portal/core/`
- SDK for Apps: `sdk/`
- Tunnel client: `cmd/portal-tunnel/` (exposes local services)
- Webclient: `cmd/webclient/` (WASM + service worker served by relay)
- Admin frontend: `cmd/relay-server/frontend/` (built into `cmd/relay-server/dist/app`)

## Connection Flow (High Level)

1. App registers a Lease with the relay (identity, ALPN, metadata).
2. Client requests connection by Lease ID or name.
3. Relay forwards the request to the App and brokers the connection.
4. RDSEC handshake establishes end-to-end encryption (X25519 + ChaCha20-Poly1305).
5. Yamux multiplexes multiple streams over one relay connection.

## Key Terms

- Portal / Relay: central mediator; never decrypts payloads.
- App: service publisher using SDK or tunnel to register Leases.
- Client: consumer (often browser + WASM) connecting via relay.
- Lease: advertising unit; one Lease maps to one public endpoint.

## Where to Look

- `cmd/relay-server/` (entrypoint and HTTP/WS relay)
- `portal/` (core relay logic)
- `sdk/` (App integration)
- `cmd/portal-tunnel/` (tunnel client)
- `cmd/webclient/` (WASM client)
- `docs/architecture.md` and `docs/glossary.md`

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
