# Gosuda Template for Go

Official AI agent coding guidelines and tooling templates for Go projects under [github.com/gosuda](https://github.com/gosuda).

## What's Included

| File | Purpose |
|------|---------|
| [`AGENTS.md`](AGENTS.md) | AI agent coding guidelines (Go 1.25+) |
| [`CLAUDE.md`](CLAUDE.md) | Symlink → `AGENTS.md` (Claude Code compatibility) |
| [`.golangci.yml`](.golangci.yml) | golangci-lint v2 config — 41 linters across 4 tiers |
| [`Makefile`](Makefile) | Build, lint, test, vuln scan targets |
| [`.github/workflows/ci.yml`](.github/workflows/ci.yml) | GitHub Actions: test → lint → security → build |

## Usage

### New Project Setup

1. **Copy config files** into your Go project root:

   ```bash
   # From a clone of this repo
   cp .golangci.yml Makefile /path/to/your/project/
   cp -r .github /path/to/your/project/
   ```

2. **Copy agent guidelines** (for AI-assisted development):

   ```bash
   cp AGENTS.md /path/to/your/project/
   ln -s AGENTS.md /path/to/your/project/CLAUDE.md
   ```

3. **Verify setup:**

   ```bash
   cd /path/to/your/project
   make all
   ```

### As a GitHub Template

This repo is designed as a **template repository**. Click **"Use this template"** on GitHub to create a new project with all configs pre-applied.

## Tooling Requirements

| Tool | Install |
|------|---------|
| Go 1.25+ | [go.dev/dl](https://go.dev/dl/) |
| golangci-lint v2 | `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest` |
| govulncheck | `go install golang.org/x/vuln/cmd/govulncheck@latest` |
| goimports | `go install golang.org/x/tools/cmd/goimports@latest` |

## CI Pipeline

```
test (race + coverage) ─┐
lint (golangci-lint v2) ─┼─→ build
security (govulncheck) ─┘
```

All three jobs run in parallel; build depends on all passing.

## Linter Tiers

- **Tier 1 — Correctness** (14): govet, errcheck, staticcheck, unused, gosec, errorlint, nilerr, copyloopvar, bodyclose, sqlclosecheck, rowserrcheck, durationcheck, makezero, noctx
- **Tier 2 — Quality** (16): gocritic (all tags), revive, unconvert, unparam, wastedassign, misspell, whitespace, godot, goconst, dupword, usestdlibvars, testifylint, testableexamples, tparallel, usetesting
- **Tier 3 — Concurrency** (3): gochecknoglobals, gochecknoinits, containedctx
- **Tier 4 — Performance** (9): prealloc, intrange, modernize, fatcontext, perfsprint, reassign, spancheck, mirror, recvcheck

## License

Internal tooling for [gosuda](https://github.com/gosuda) projects.
