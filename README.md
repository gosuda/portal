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

## Quick Start

```bash
# Run the relay server
go run ./cmd/relay-server/ --port 4017 --tls-auto

# In another terminal, expose a local service
go run ./cmd/portal-tunnel/ --relay https://localhost:4017/relay --local localhost:8080
```

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
