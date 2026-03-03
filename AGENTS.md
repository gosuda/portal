# AGENTS.md

## Formatting & Style

**Mandatory** before every commit: `gofmt -w . && goimports -w .`

Import ordering: **stdlib → external → internal** (blank-line separated). Local prefix: `github.com/gosuda`.

**Naming:** packages lowercase single-word (`httpwrap`) · interfaces as behavior verbs (`Reader`, `Handler`) · errors `Err` prefix sentinels (`ErrNotFound`), `Error` suffix types · context always first param `func Do(ctx context.Context, ...)`

**CGo:** always disabled — `CGO_ENABLED=0`. Pure Go only. No C dependencies.

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

---

## Module Hygiene

- **Always commit** `go.mod` and `go.sum` · **never commit** `go.work`
- **Pin toolchain:** `toolchain go1.25.0` in go.mod
- **Tool directive (Go 1.24+):** `tool golang.org/x/tools/cmd/stringer` in go.mod
- **Pre-release:** `go mod tidy && go mod verify && govulncheck ./...`
- **Sandboxed I/O (Go 1.24+):** `os.Root` for directory-scoped file operations

---

**Pre-commit:** `make all` or `gofmt -w . && goimports -w . && go vet ./... && golangci-lint run --fix && go test -race ./... && govulncheck ./...`

---

## Verbalized Sampling

Before trival or non-trivial changes, AI agents **must**:

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
