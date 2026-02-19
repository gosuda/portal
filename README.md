# Portal

Self-hosted relay that enables peer-to-peer, end-to-end encrypted connections through a central hub. Participants authenticate with Ed25519 credentials, perform a Noise XX handshake, and communicate over ChaCha20-Poly1305 encrypted channels. The relay cannot decrypt application traffic — it only forwards ciphertext.

All relay connections use **WebTransport** (HTTP/3 over QUIC) for NAT/firewall traversal.

## Binaries

| Binary | Description |
|--------|-------------|
| `cmd/relay-server` | Relay server with React admin UI, WebTransport endpoint, and subdomain routing |
| `cmd/portal-tunnel` | CLI tunnel client — registers a lease and proxies connections to a local TCP service |
| `cmd/vanity-id` | Brute-force tool for generating credential IDs matching a desired prefix |

## SDK

The `sdk/` package provides the public API for service publishers:

- `Client` manages relay connections with automatic reconnect and health checking
- `Listen()` registers a lease and returns incoming connections
- `Dial()` connects to a lease on a relay and performs the E2EE handshake

## Prerequisites

- **Go 1.26+** — [install](https://go.dev/dl/)
- **npm** — required only for the admin frontend (`cmd/relay-server/frontend/`)

## Quick Start

```bash
# Run the relay server (auto-generates self-signed TLS cert for WebTransport)
go run ./cmd/relay-server/ --port 4017 --tls-auto

# In another terminal, expose a local service
go run ./cmd/portal-tunnel/ --relay https://localhost:4017/relay --local localhost:8080
```

> **Note:** `--tls-auto` generates a self-signed ECDSA P-256 certificate valid for <14 days.
> The cert hash is available at `/cert-hash` for browser `serverCertificateHashes` pinning.
> For production, use `--tls-cert` and `--tls-key` with CA-signed certificates.

## Build Binaries

```bash
make build
# or
just build
```

Built binaries are written to `./bin`:

- `./bin/relay-server`
- `./bin/portal-tunnel`
- `./bin/demo-app`
- `./bin/vanity-id`

## Development

```bash
make all        # fmt, vet, lint, test, vuln, build, frontend
make test       # go test -v -race -coverprofile=coverage.out ./...
make lint       # golangci-lint run
make proto      # buf generate + buf lint
```

See [AGENTS.md](AGENTS.md) for architecture details and coding guidelines.

## License

See [LICENSE](LICENSE).
