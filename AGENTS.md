# AGENTS.md — Portal (gosuda.org/portal)

Portal is a self-hosted relay/tunnel that enables peer-to-peer, end-to-end encrypted connections between services through a central hub. Participants authenticate with Ed25519 credentials, perform X25519 ECDH key exchange, and communicate over ChaCha20-Poly1305 encrypted channels. The relay server cannot decrypt application traffic — it only forwards ciphertext. All relay connections use WebSocket transport for NAT/firewall traversal, and services advertise themselves via time-limited leases.

---

## Architecture

**Relay Server** (`cmd/relay-server/`) — Single binary HTTP server. Accepts WebSocket connections at `/relay`, serves the React SPA at `/app/`, exposes admin REST API at `/admin/`, and routes subdomain traffic to apps via a portal mux. Manages approval, BPS rate limiting, and IP tracking through `cmd/relay-server/manager/`.

**Portal library** (`portal/`) — Core protocol implementation. `RelayServer` accepts connections and dispatches yamux streams by packet type. `RelayClient` manages the app-side yamux session. `LeaseManager` handles lease registration, renewal, expiry, bans, and name policies. Crypto operations live in `portal/core/cryptoops/`: `Credential` (Ed25519 keygen + identity derivation), `Handshaker` (X25519 ECDH), and `SecureConnection` (ChaCha20-Poly1305 stream wrapper). Protocol messages are defined as protobuf in `portal/core/proto/rdverb/` (protocol verbs) and `portal/core/proto/rdsec/` (security payloads).

**SDK** (`sdk/`) — Public API for service publishers. `Client` manages relay connections with automatic reconnect and health checking. `Listen()` registers a lease and returns incoming connections. `Dial()` connects to a lease on a relay and performs the E2EE handshake.

**portal-tunnel** (`cmd/portal-tunnel/`) — CLI tunnel client. Uses `sdk.Listen()` to register a lease, then proxies incoming relay connections to a local TCP service.

**webclient** (`cmd/webclient/`) — Go compiled to WASM for browser-native relay connections. Includes a service worker that intercepts HTTP fetches and proxies them through the E2EE relay. Browser WebSocket adapter in `cmd/webclient/wsjs/`, HTTP interception in `cmd/webclient/httpjs/`.

**vanity-id** (`cmd/vanity-id/`) — Brute-force tool for generating credential IDs matching a desired prefix.

### Key Abstractions

- **Credential** — Ed25519 keypair. Identity ID = `HMAC-SHA256(pubkey, magic)[0:16]` encoded as base32 (26 chars). Every participant has one.
- **Lease** — Advertising slot: identity + TTL (30s, renewed every 5s) + name + ALPN list + JSON metadata. One lease = one public endpoint.
- **yamux** — Multiplexes streams over a single WebSocket connection. Each protocol operation (lease update, connection request) opens a new stream.
- **SecureConnection** — Wraps `io.ReadWriteCloser` with authenticated encryption. Length-prefixed frames: `[4B len][12B nonce][ciphertext+tag]`.

### Data Flow

App connects via WebSocket → yamux session established → App registers lease (signed with Ed25519) → Client requests connection to a lease ID → Relay bridges two yamux streams → Client and App perform X25519 handshake through the relay → All subsequent data is ChaCha20-Poly1305 encrypted end-to-end (relay forwards opaque ciphertext).

---

## Build & Development

**Makefile targets:**

| Target | Command |
|--------|---------|
| `make fmt` | `gofmt -w . && goimports -w .` |
| `make vet` | `go vet ./...` |
| `make lint` | `golangci-lint run` |
| `make test` | `go test -v -race -coverprofile=coverage.out ./...` |
| `make vuln` | `govulncheck ./...` |
| `make tidy` | `go mod tidy && go mod verify` |
| `make build` | `go build ./...` |
| `make all` | `fmt vet lint test vuln build` |

**justfile** provides alternative targets: `just fmt` runs golangci-lint auto-fix before gofmt, `just lint` skips errcheck, `just lint-fix` runs golangci-lint with `--fix`.

**Run a single package's tests:** `go test -race -v ./portal/...` or `go test -race -v ./sdk/...`

**Run the relay server locally:** `go run ./cmd/relay-server/ --port 4017`

**Docker:** `docker compose up` — builds and runs on port 4017.

**Pre-commit:** `make all`

### Frontend

React 19 + Vite 7 + Tailwind v4 + shadcn/ui (Radix primitives). Source: `cmd/relay-server/frontend/`.

```bash
cd cmd/relay-server/frontend && npm install
npm run dev      # Vite dev server
npm run build    # Production build (tsc + vite build)
```

### Proto Generation

Protobuf schemas live in `proto/` (not committed generated code path — generated into `portal/core/proto/`). Uses buf v2 with `protoc-gen-go` and `protoc-gen-go-vtproto` (fast marshaling).

```bash
buf generate    # regenerate from proto/ into portal/core/proto/
buf lint        # lint proto files (STANDARD rules)
```

Two proto packages:
- `rdsec` — security: `Identity`, `SignedPayload`, `ClientInitPayload`, `ServerInitPayload`
- `rdverb` — protocol: `Packet`, `Lease`, `RelayInfoRequest/Response`, `LeaseUpdateRequest/Response`, `ConnectionRequest/Response`

---

## Server Configuration

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--port` | — | `4017` | HTTP listen port |
| `--portal-url` | `PORTAL_URL` | `http://localhost:4017` | Base URL for frontend |
| `--portal-app-url` | `PORTAL_APP_URL` | derived from portal-url | Wildcard subdomain URL |
| `--bootstraps` | `BOOTSTRAP_URIS` | derived from portal-url | WebSocket relay endpoints (comma-separated) |
| `--admin-secret-key` | `ADMIN_SECRET_KEY` | auto-generated | Admin auth key (logged if auto-generated) |
| `--max-lease` | — | `0` (unlimited) | Max active relayed connections per lease |
| `--lease-bps` | — | `0` (unlimited) | Default bytes-per-second limit per lease |
| `--noindex` | `NOINDEX` | `false` | Block crawlers via robots.txt |
| `--alpn` | — | `http/1.1` | ALPN identifier |

Admin settings (ban list, BPS limits, approval mode, IP bans) persist to `admin_settings.json`.

---

## Key Dependencies

| Package | Role |
|---------|------|
| `github.com/gorilla/websocket` | WebSocket client + server |
| `github.com/hashicorp/yamux` | Stream multiplexing over single connection |
| `github.com/planetscale/vtprotobuf` | Fast protobuf marshaling (generated vtproto code) |
| `github.com/rs/zerolog` | Structured JSON logging |
| `github.com/valyala/bytebufferpool` | Pooled byte buffers (reduces GC pressure in SecureConnection) |
| `golang.org/x/crypto` | ChaCha20-Poly1305, Curve25519, HKDF |
| `gopkg.eu.org/broccoli` | CLI flag/env binding (portal-tunnel) |

---

## CI Pipeline

```
check-go (detect go.mod) ──→ test (race + coverage) ─┐
                          ──→ lint (golangci-lint v2) ─┼─→ build
                          ──→ security (govulncheck)  ─┘
```

Triggers on every push and PR. All three verification jobs run in parallel; build runs after all pass.

CD (on push to main/tags): builds multi-platform Docker image → pushes to `ghcr.io/gosuda/portal` → deploys to Kubernetes with automatic rollback on failure.

---

## Formatting & Style

**Mandatory** before every commit: `gofmt -w . && goimports -w .`

Import ordering: **stdlib → external → internal** (blank-line separated). Local prefix: `github.com/gosuda`.

**Naming:** packages lowercase single-word (`httpwrap`) · interfaces as behavior verbs (`Reader`, `Handler`) · errors `Err` prefix sentinels (`ErrNotFound`), `Error` suffix types · context always first param `func Do(ctx context.Context, ...)`

**CGo:** always disabled — `CGO_ENABLED=0`. Pure Go only. No C dependencies.

---

## Static Analysis & Linters

| Tool | Command |
|------|---------|
| Built-in vet | `go vet ./...` |
| golangci-lint v2 | `golangci-lint run` |
| Race detector | `go test -race ./...` |
| Vulnerability scan | `govulncheck ./...` |

Full configuration: **[`.golangci.yml`](.golangci.yml)**. Linter tiers:

- **Correctness** — `govet`, `errcheck`, `staticcheck`, `unused`, `gosec`, `errorlint`, `nilerr`, `copyloopvar`, `bodyclose`, `sqlclosecheck`, `rowserrcheck`, `durationcheck`, `makezero`, `noctx`
- **Quality** — `gocritic` (all tags), `revive`, `unconvert`, `unparam`, `wastedassign`, `misspell`, `whitespace`, `godot`, `goconst`, `dupword`, `usestdlibvars`, `testifylint`, `testableexamples`, `tparallel`, `usetesting`
- **Concurrency safety** — `gochecknoglobals`, `gochecknoinits`, `containedctx`
- **Performance & modernization** — `prealloc`, `intrange`, `modernize`, `fatcontext`, `perfsprint`, `reassign`, `spancheck`, `mirror`, `recvcheck`

---

## Error Handling

1. **Wrap with `%w`** — always add call-site context: `return fmt.Errorf("repo.Find: %w", err)`
2. **Sentinel errors** per package: `var ErrNotFound = errors.New("user: not found")`
3. **Multi-error** — use `errors.Join(err1, err2)` or `fmt.Errorf("op: %w and %w", e1, e2)`
4. **Never ignore errors** — `_ = fn()` only for `errcheck.exclude-functions`
5. **Fail fast** — return immediately; no state accumulation after failure
6. **Check with `errors.Is`/`errors.As`** — never string-match `err.Error()`

---

## Iterators (Go 1.23+)

Signatures: `func(yield func() bool)` · `func(yield func(V) bool)` · `func(yield func(K, V) bool)`

**Rules:** always check yield return (panics on break if ignored) · avoid defer/recover in iterator bodies · use stdlib (`slices.All`, `slices.Backward`, `slices.Collect`, `maps.Keys`, `maps.Values`) · range over integers: `for i := range n {}`

---

## Context & Concurrency

Every public I/O function **must** take `context.Context` first.

| Pattern | Primitive |
|---------|-----------|
| Parallel work with errors | `errgroup.Group` (preferred over `WaitGroup`) |
| Bounded concurrency | `errgroup.SetLimit` or buffered channel semaphore |
| Fan-out/fan-in | Unbuffered chan + N producers + 1 consumer; `select` to merge |
| Pipeline stages | `chan T` between stages, sender closes to signal done |
| Cancellation/timeout | `context.WithCancel` / `context.WithTimeout` |
| Concurrent read/write | `sync.RWMutex` (encapsulate behind methods) |
| Lock-free counters | `atomic.Int64` / `atomic.Uint64` |
| One-time init | `sync.Once` / `sync.OnceValue` / `sync.OnceFunc` |
| Object reuse | `sync.Pool` (hot paths only, no lifetime guarantees) |

**Goroutine rules:** creator owns lifecycle (start, stop, errors, panic recovery) · no bare `go func()` · every goroutine needs a clear exit (context, done channel, bounded work) · leaks are bugs — verify with `goleak` or `runtime.NumGoroutine()`

**Channel rules:** use directional types (`chan<-`/`<-chan`) in signatures · only sender closes · nil channel blocks forever (use to disable `select` cases) · unbuffered = synchronization, buffered = decoupling/backpressure · `for v := range ch` until closed · `select` with `default` only for non-blocking try-send/try-receive

**Select patterns:** timeout via `context.WithTimeout` (not `time.After` in loops — leaks timers) · always check `ctx.Done()` · fan-in merges with multi-case `select` · rate-limit with `time.Ticker` not `time.Sleep`

```go
g, ctx := errgroup.WithContext(ctx)
g.SetLimit(maxWorkers)
for _, item := range items {
    g.Go(func() error { return process(ctx, item) })
}
if err := g.Wait(); err != nil { return fmt.Errorf("processAll: %w", err) }
```

**Anti-patterns:** ❌ shared memory without sync · ❌ `sync.Mutex` in public APIs · ❌ goroutine without context · ❌ closing channel from receiver · ❌ sending on closed channel · ❌ `time.Sleep` for synchronization · ❌ unbounded goroutine spawn

---

## Testing

```bash
go test -v -race -coverprofile=coverage.out ./...
```

- **Benchmarks (Go 1.24+):** `for b.Loop() {}` — prevents compiler opts, excludes setup from timing
- **Test contexts (Go 1.24+):** `ctx := t.Context()` — auto-canceled when test ends
- **Table-driven tests** as default · **race detection** (`-race`) mandatory in CI
- **Fuzz testing:** `go test -fuzz=. -fuzztime=30s` — fast, deterministic targets
- **testify** for assertions when stdlib `testing` is verbose

---

## Security

- **Vulnerability scanning:** `govulncheck ./...` — CI and pre-release
- **Module integrity:** `go mod verify` — validates checksums against go.sum
- **Supply chain:** always commit `go.sum` · audit with `go mod graph` · pin toolchain
- **SBOM:** `syft packages . -o cyclonedx-json > sbom.json` on release
- **Crypto:** FIPS 140-3, post-quantum X25519MLKEM768, `crypto/rand.Text()` for secure tokens

---

## Performance

- **Object reuse:** `sync.Pool` hot paths · `weak.Make` for cache-friendly patterns
- **Benchmarking:** `go test -bench=. -benchmem` · `-cpuprofile`/`-memprofile`
- **Avoid `reflect`:** ~30x slower than static code, defeats compile-time checks and linters · prefer generics (4–18x faster), type switches, interfaces, or `go generate` codegen for hot paths
- **Escape analysis:** `go build -gcflags='-m'` to verify heap allocations

* **PGO:** production CPU profile → `default.pgo` in main package → rebuild (2–14% gain)
* **GOGC:** default 100; high-throughput `200-400`; memory-constrained `GOMEMLIMIT` + `GOGC=off`

---

## Module Hygiene

- **Always commit** `go.mod` and `go.sum` · **never commit** `go.work`
- **Pin toolchain:** `toolchain go1.25.0` in go.mod
- **Tool directive (Go 1.24+):** `tool golang.org/x/tools/cmd/stringer` in go.mod
- **Pre-release:** `go mod tidy && go mod verify && govulncheck ./...`
- **Sandboxed I/O (Go 1.24+):** `os.Root` for directory-scoped file operations

---

## CI/CD & Tooling

| File | Purpose |
|------|---------|
| [`.golangci.yml`](.golangci.yml) | golangci-lint v2 configuration |
| [`Makefile`](Makefile) | Build/lint/test/vuln targets |
| [`.github/workflows/ci.yml`](.github/workflows/ci.yml) | GitHub Actions: test → lint → security → build |

---

## Verbalized Sampling

Before trivial or non-trivial changes, AI agents **must**:

1. **Sample 3–5 intent hypotheses** — rank by likelihood, note one weakness each
2. **Explore edge cases** — up to 3 standard, 5 for architectural changes
3. **Assess coupling** — structural (imports), temporal (co-changing files), semantic (shared concepts)
4. **Tidy first** — high coupling → extract/split/rename before changing; low → change directly
5. **Surface decisions** — ask the human when trade-offs exist; do exactly what is asked, no more
