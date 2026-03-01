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

Portal is a relay network that connects Apps (service publishers) and Clients (service consumers) through a central relay server without decrypting payloads.

Core components:
- Relay server: `cmd/relay-server` (HTTP API/admin + SNI router)
- Relay core logic: `portal/` (lease manager, reverse connection hub, forwarding)
- SNI router package: `portal/sni/`
- SDK for Apps: `sdk/`
- Tunnel client: `cmd/portal-tunnel/` (exposes local services)
- Admin frontend: `cmd/relay-server/frontend/` (built into `cmd/relay-server/dist/app`)

## Connection Flow (High Level)

1. App/Tunnel registers a Lease with relay via `/sdk/register` (name, metadata, TLS mode, reverse token).
2. Tunnel maintains reverse WebSocket workers to relay via `/sdk/connect`.
3. Client traffic enters relay:
   - TLS traffic on SNI port is routed by SNI.
   - Non-TLS traffic can use HTTP proxy mode.
4. Relay acquires a reverse tunnel connection and forwards bytes end-to-end.

## Key Terms

- Portal / Relay: central mediator; never decrypts payloads.
- App: service publisher using SDK or tunnel to register Leases.
- Client: consumer connecting via relay.
- Lease: advertising unit; one Lease maps to one public endpoint.

## Where to Look

- `cmd/relay-server/` (entrypoint, HTTP APIs, SNI callback wiring)
- `portal/reverse_hub.go` (reverse WebSocket connection pool)
- `portal/sni/` (SNI parser/router)
- `sdk/` (App integration)
- `cmd/portal-tunnel/` (tunnel client)
- `docs/architecture.md` and `docs/glossary.md`

## Domain Configuration

Portal uses environment variables for domain and TLS configuration:

### Core Environment Variables

| Variable | Description |
|----------|-------------|
| `PORTAL_URL` | Base URL (e.g., `https://portal.example.com`) |
| `BOOTSTRAP_URIS` | Relay API URLs (defaults to `PORTAL_URL`) |
| `SNI_PORT` | SNI router port (default `:443`) |
| `ADMIN_SECRET_KEY` | Admin auth key (auto-generated if unset) |

### Tunnel Environment Variables

| Variable | Description |
|----------|-------------|
| `RELAYS` | Relay API URLs for tunnel client (comma-separated) |
| `TLS_MODE` | `no-tls`, `self`, or `keyless` |
| `TLS_CERT_FILE` | Self TLS certificate chain path (self mode only) |
| `TLS_KEY_FILE` | Self TLS private key path (self mode only) |

### Domain Derivation

- Service URL: `{name}.{base_domain}` (e.g., `myapp.example.com`)
- Base domain extracted from `PORTAL_URL` via `extractBaseDomain()` in `cmd/relay-server/utils.go`
- SNI routes registered in `portal/sni/router.go`

### TLS Modes

1. **`no-tls`**: HTTP proxy mode for development.
2. **`self`**: Tunnel uses locally provided certificate and key (`TLS_CERT_FILE` + `TLS_KEY_FILE`).
3. **`keyless`**: Tunnel uses SDK keyless mode with auto defaults.
   - Keyless signer endpoint defaults to relay URL unless explicitly overridden in SDK options.
   - Certificate chain/root trust are auto-discovered by SDK from signer endpoint when not explicitly provided.
   - Auto-discovery requires an HTTPS signer endpoint.

See `docs/portal-deploy-guide.md` for full deployment documentation.

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
