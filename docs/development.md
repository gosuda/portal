# Development Guide

## Quick Start

```bash
# Clone and build
git clone https://github.com/gosuda/portal.git
cd portal
make build

# Run tests
make test

# Run relay server locally
make run
```

## Project Structure

```
cmd/
  relay-server/    # Relay server entrypoint
  portal-tunnel/   # Tunnel CLI client
  demo-app/        # Demo application
portal/            # Core relay logic
sdk/               # Go SDK for apps
utils/             # Shared utilities
```

## Key Commands

| Command | Description |
|---------|-------------|
| `make build` | Build all components |
| `make test` | Run tests with race detector |
| `make lint` | Run golangci-lint |
| `make fmt` | Format code |
| `make run` | Run relay server |

## Guidelines

1. **No breaking changes** to existing workflows without team discussion
2. **Test before merging** — all features must be verified in a branch
3. **Follow existing patterns** — check similar code before adding new features
4. **Run linters** before committing: `make fmt && make lint`

## Architecture Decisions

- **E2EE**: TLS passthrough with keyless certificates
- **SNI Routing**: TLS routed by hostname without termination
- **No CGO**: pure Go for cross-platform builds
