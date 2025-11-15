# portal-tunnel User Guide

`portal-tunnel` is a tunneling tool that registers locally running services to a Portal relay server, allowing external access through the `/peer/<service-name>` path. A single process can manage multiple relays and multiple services simultaneously.

## Requirements

* Go 1.25 or later
* Portal relay server URL (e.g., `wss://portal.gosuda.org/relay`)
* Local TCP-based services to expose through the relay (e.g., HTTP, gRPC)

## Running the Tool

### Build the binary

```bash
go build -o bin/portal-tunnel ./cmd/portal-tunnel
bin/portal-tunnel expose --help
```

### Run with `go run`

```bash
go run ./cmd/portal-tunnel expose \
  --relay ws://localhost:4017/relay \
  --host localhost \
  --port 4018
```

## Using a Configuration File

1. Copy the example configuration:

   ```bash
   cp cmd/portal-tunnel/config.yaml.example portal-tunnel.yaml
   ```

2. Update relay and service definitions in `portal-tunnel.yaml`.

3. Start the tunnel:

   ```bash
   bin/portal-tunnel expose --config portal-tunnel.yaml
   ```

4. To run only a specific service, add the `--service <name>` flag.

### config.yaml Fields

```yaml
relays:
  - name: gosuda
    urls:
      - wss://portal.gosuda.org.kr/relay

services:
  - name: my-api
    relayPreference:        # Relays are attempted in the listed order.
      - gosuda
    target: localhost:8080  # Local proxy target (host:port)
    protocols:              # Optional; defaults to ["http/1.1"]
      - http/1.1
      - h2
```

* `relays`: List of relay servers. Each entry must include `name` and one or more `urls`.
* `services`: List of local services to expose.

  * `name`: Service name to register with the relay. If omitted, a name is generated as `tunnel-<lease-id>`.
  * `relayPreference`: Ordered list of relay names. Unknown names are ignored; at least one valid URL must remain.
  * `target`: Local proxy target (`host:port`).
  * `protocols`: ALPN protocol list. Defaults to `http/1.1` if omitted.

The process gracefully shuts down all tunnels when it receives `SIGINT` or `SIGTERM`.

## Running a Single Service with Flags

You can expose a single temporary service without a configuration file.

```bash
bin/portal-tunnel expose \
  --relay wss://portal.gosuda.org.kr/relay \
  --host localhost \
  --port 8080 \
  --name dev-api
```

* `--relay`: Required. WebSocket URL of the relay server.
* `--host`, `--port`: Local service address to proxy. Defaults to `localhost:4018`.
* `--name`: Public service name. Auto-generated if omitted.

## Verifying Access

When the tunnel is established, the log prints the accessible URL.

* Access via `/peer/<service-name>` or `/peer/<lease-id>`.
* Example: `http://portal.gosuda.org.kr/peer/dev-api`

Relay logs show connection events (`->`) and disconnect events (`<-`), allowing real-time monitoring.
