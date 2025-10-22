# CI/CD Setup Guide

This document describes the CI/CD infrastructure for the RelayDNS project, including pre-commit hooks, linters, formatters, sanitizers, and GitHub Actions workflows.

## Table of Contents

- [Overview](#overview)
- [Quick Start](#quick-start)
- [Tools](#tools)
- [Pre-commit Hooks](#pre-commit-hooks)
- [GitHub Actions](#github-actions)
- [Makefile Targets](#makefile-targets)
- [Configuration Files](#configuration-files)
- [Troubleshooting](#troubleshooting)

## Overview

The CI/CD setup includes:

1. **Pre-commit hooks** - Run checks locally before commits
2. **golangci-lint** - Comprehensive Go linting with 30+ linters
3. **Formatters** - gofmt, goimports, gci
4. **Sanitizers** - Race detector, staticcheck, gosec, govulncheck
5. **GitHub Actions** - Automated CI pipeline on push/PR

## Quick Start

### 1. Install Required Tools

```bash
# Install Go tools
make install-tools

# Install golangci-lint
# See: https://golangci-lint.run/usage/install/
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin v2.5.0

# Install pre-commit (Python required)
pip install pre-commit

# Install pre-commit hooks
make pre-commit-install
```

### 2. Run Local CI Checks

```bash
# Run all local CI checks (fmt, tidy, vet, lint, test-race)
make ci-local

# Or run individual checks
make fmt           # Format code
make lint          # Run linters
make test-race     # Run tests with race detector
```

### 3. Commit Changes

After installing pre-commit hooks, every commit will automatically run:
- File checks (trailing whitespace, EOF, YAML validation)
- Go formatting (gofmt, goimports)
- Go vet
- golangci-lint
- Build check (ensure all binaries compile)
- Tests with race detector

```bash
git add .
git commit -m "Your commit message"
# Pre-commit hooks will run automatically
```

## Tools

### golangci-lint

Configuration: `.golangci.yml`

Enabled linters:
- **Formatters**: gofmt, goimports, gci
- **Core**: govet, staticcheck, errcheck, gosimple, ineffassign, unused
- **Style**: revive, stylecheck
- **Security**: gosec, bodyclose, noctx, rowserrcheck, sqlclosecheck
- **Complexity**: gocyclo, gocognit, cyclop, nestif
- **Error handling**: errname, errorlint
- **Performance**: prealloc
- **Code quality**: dupl, goconst, unconvert, unparam, nakedret, misspell

Usage:
```bash
# Run all linters
make lint

# Run with auto-fix
make lint-fix

# Run golangci-lint directly
golangci-lint run --timeout=5m --config=.golangci.yml
```

### Formatters

**gofmt** - Standard Go formatter
```bash
go fmt ./...
```

**goimports** - Organizes imports
```bash
goimports -local github.com/gosuda/relaydns -w .
```

**gci** - Controls import order (integrated in golangci-lint)
- Standard library imports
- Third-party imports
- Local imports (github.com/gosuda/relaydns)

### Sanitizers

**Race Detector** - Detects data races
```bash
make test-race
go test -race ./...
```

**staticcheck** - Advanced static analysis
```bash
make staticcheck
staticcheck ./...
```

**gosec** - Security scanner
```bash
make gosec
gosec ./...
```

**govulncheck** - Vulnerability scanner
```bash
make govulncheck
govulncheck ./...
```

## Pre-commit Hooks

Configuration: `.pre-commit-config.yaml`

Hooks run automatically before each commit:

1. **General file checks**
   - Trailing whitespace
   - End of file fixer
   - YAML validation
   - Large file check (max 1MB)
   - Merge conflict detection
   - Private key detection

2. **Go checks**
   - `go fmt ./...`
   - `goimports -local github.com/gosuda/relaydns`
   - `go vet ./...`
   - `go mod tidy` (ensures go.mod is clean)
   - `golangci-lint run`

3. **Build verification**
   - Compile all binaries (server, client, chat)

4. **Tests**
   - Run all tests with race detector

5. **Additional linting**
   - YAML linting (yamllint)
   - Dockerfile linting (hadolint)

### Managing Pre-commit Hooks

```bash
# Install hooks
make pre-commit-install

# Run manually on all files
make pre-commit-run
pre-commit run --all-files

# Run manually on staged files
pre-commit run

# Update hooks to latest versions
pre-commit autoupdate

# Skip hooks for a commit (use sparingly)
git commit --no-verify -m "Emergency fix"
```

## GitHub Actions

Configuration: `.github/workflows/ci.yml`

Triggered on:
- Push to `main` or `develop` branches
- Pull requests to `main` or `develop`
- Manual workflow dispatch

### Jobs

#### 1. Lint Job
- Runs on: `ubuntu-latest`
- Go version: `1.25.0`
- Steps:
  - Verify dependencies
  - Run gofmt check
  - Run goimports check
  - Run go vet
  - Run golangci-lint
  - Check go.mod tidiness

#### 2. Build Job
- Runs on: `ubuntu-latest`
- Go versions: `1.25.0`, `1.24.x` (matrix)
- Steps:
  - Build server binary
  - Build example HTTP client
  - Build example chat client
  - Upload binaries (artifacts, 7 days retention)

#### 3. Test Job
- Runs on: `ubuntu-latest`
- Go versions: `1.25.0`, `1.24.x` (matrix)
- Steps:
  - Run tests with race detector
  - Generate coverage report
  - Upload coverage HTML (7 days retention)
  - Check coverage threshold (warning if < 30%)

#### 4. Sanitizers Job
- Runs on: `ubuntu-latest`
- Go version: `1.25.0`
- Steps:
  - Run staticcheck
  - Run gosec (security scanner)
  - Upload gosec report (JSON)
  - Check for ineffective assignments
  - Check for unused code

#### 5. Docker Job
- Runs on: `ubuntu-latest`
- Steps:
  - Build Docker image
  - Use buildx for caching
  - Validate Dockerfile

#### 6. Dependencies Job
- Runs on: `ubuntu-latest`
- Go version: `1.25.0`
- Steps:
  - Run govulncheck (vulnerability scanner)
  - Check for outdated dependencies

### Viewing Results

- Go to GitHub Actions tab in your repository
- Click on a workflow run to see job details
- Download artifacts (binaries, coverage reports) from workflow summary

## Makefile Targets

### Development

```bash
make fmt              # Format Go code with gofmt and goimports
make tidy             # Tidy go.mod
make vet              # Run go vet
```

### Linting

```bash
make lint             # Run golangci-lint
make lint-fix         # Run golangci-lint with auto-fix
```

### Testing

```bash
make test             # Run unit tests
make test-race        # Run tests with race detector
make test-coverage    # Run tests with coverage report (generates coverage.html)
```

### Static Analysis & Security

```bash
make staticcheck      # Run staticcheck
make gosec            # Run gosec security scanner
make govulncheck      # Check for known vulnerabilities
```

### Build

```bash
make build-all        # Build all binaries (server, client, chat)
make client-build     # Build example HTTP client
make chat-build       # Build example chat client
```

### Pre-commit

```bash
make pre-commit-install  # Install pre-commit hooks
make pre-commit-run      # Run pre-commit on all files
```

### CI/CD

```bash
make ci-local         # Run local CI checks (fmt, tidy, vet, lint, test-race)
make install-tools    # Install development tools
make clean            # Remove build artifacts and reports
```

## Configuration Files

### `.golangci.yml`

Main linter configuration with 30+ linters enabled.

Key settings:
- Timeout: 5 minutes
- Go version: 1.25
- Local prefix: `github.com/gosuda/relaydns`
- Excludes test files from certain linters
- Excludes example code from security checks

### `.pre-commit-config.yaml`

Pre-commit hooks configuration.

Repos:
- `pre-commit/pre-commit-hooks`: General file checks
- `dnephin/pre-commit-golang`: Go formatting and checks
- `golangci/golangci-lint`: Comprehensive linting
- `local`: Build and test checks
- `adrienverge/yamllint`: YAML linting
- `hadolint/hadolint`: Dockerfile linting

### `.yamllint.yml`

YAML linting configuration.

Settings:
- Max line length: 120 (warning level)
- Indentation: 2 spaces
- Document start: disabled
- Truthy values: `true`, `false`, `on`, `off`

### `.github/workflows/ci.yml`

GitHub Actions workflow definition with 6 jobs:
1. Lint
2. Build
3. Test
4. Sanitizers
5. Docker
6. Dependencies

## Troubleshooting

### golangci-lint fails with "deadline exceeded"

Increase timeout:
```bash
golangci-lint run --timeout=10m
```

Or edit `.golangci.yml`:
```yaml
run:
  timeout: 10m
```

### Pre-commit hooks are slow

Skip build and test checks for faster commits:
```bash
SKIP=go-build,go-test git commit -m "Quick fix"
```

Or disable specific hooks in `.pre-commit-config.yaml`.

### GitHub Actions fails on Go 1.24.x

The project requires Go 1.25.0 features. Consider removing Go 1.24.x from the matrix if incompatible.

Edit `.github/workflows/ci.yml`:
```yaml
strategy:
  matrix:
    go-version: ['1.25.0']  # Remove 1.24.x
```

### Pre-commit installation fails

Ensure Python and pip are installed:
```bash
python3 --version
pip --version
pip install --user pre-commit
```

### golangci-lint not found

Install golangci-lint:
```bash
# Linux/macOS
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin v2.5.0

# Or using Go
go install github.com/golangci/golangci-lint/cmd/golangci-lint@v2.5.0

# Verify installation
golangci-lint --version
```

### Tests fail with race detector

Race detector found a data race. Fix the race condition in your code:
1. Review the race detector output
2. Identify the conflicting goroutines
3. Add proper synchronization (mutex, channel, atomic)
4. Re-run tests

### Coverage report not generated

Ensure you have write permissions and run:
```bash
make test-coverage
# Opens coverage.html in browser
open coverage.html  # macOS
xdg-open coverage.html  # Linux
```

## Best Practices

1. **Always run `make ci-local` before pushing**
   - Catches issues early
   - Reduces CI failures

2. **Fix linter warnings**
   - Don't disable linters without good reason
   - Use `//nolint:lintername` sparingly with explanation

3. **Maintain test coverage**
   - Aim for > 50% coverage
   - Write tests for critical paths
   - Use table-driven tests

4. **Keep dependencies updated**
   - Run `make govulncheck` regularly
   - Update vulnerable dependencies promptly

5. **Use pre-commit hooks**
   - Prevents committing broken code
   - Enforces code quality standards

6. **Review GitHub Actions failures**
   - Don't merge PRs with failing checks
   - Investigate root causes, don't just re-run

## Additional Resources

- [golangci-lint documentation](https://golangci-lint.run/)
- [pre-commit documentation](https://pre-commit.com/)
- [GitHub Actions documentation](https://docs.github.com/en/actions)
- [Go race detector](https://go.dev/doc/articles/race_detector)
- [staticcheck documentation](https://staticcheck.io/)
